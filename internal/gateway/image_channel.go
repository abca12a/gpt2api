package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/apikey"
	"github.com/432539/gpt2api/internal/billing"
	"github.com/432539/gpt2api/internal/channel"
	imagepkg "github.com/432539/gpt2api/internal/image"
	modelpkg "github.com/432539/gpt2api/internal/model"
	"github.com/432539/gpt2api/internal/upstream/adapter"
	"github.com/432539/gpt2api/internal/usage"
	"github.com/432539/gpt2api/pkg/logger"
)

// dispatchImageToChannel 尝试把图片生成请求路由到外置渠道(OpenAI/Gemini 等)。
//
// 返回:
//   - handled=true:已完成响应(成功或失败),调用方直接返回;
//   - handled=false:没有可用渠道映射,调用方可以回退到内置 ChatGPT 账号池。
//
// 当前覆盖文生图、JSON 参考图、multipart edits 以及 chat->image 的外置
// image channel 路径。只有没有启用的 image 路由时才回退到内置 ChatGPT
// 账号池；一旦匹配到启用路由，调用失败会按渠道错误处理并记录健康状态，
// 不会再静默切回内置 Runner。
func (h *ImagesHandler) dispatchImageToChannel(c *gin.Context,
	ak *apikey.APIKey, m *modelpkg.Model, req *ImageGenRequest,
	rec *usage.Log, ratio float64, refs []imagepkg.ReferenceImage,
) bool {
	if h.Channels == nil {
		return false
	}
	routes, err := h.Channels.Resolve(c.Request.Context(), m.Slug, channel.ModalityImage)
	if err != nil {
		if errors.Is(err, channel.ErrNoRoute) {
			return false
		}
		logger.L().Warn("channel resolve image", zap.Error(err), zap.String("model", m.Slug))
		return false
	}
	if len(routes) == 0 {
		return false
	}

	refID := uuid.NewString()
	rec.RequestID = refID

	cost := billing.ComputeImageCost(m, req.N, ratio)
	if cost > 0 {
		if err := h.Billing.PreDeduct(c.Request.Context(), ak.UserID, ak.ID, cost, refID, "image prepay"); err != nil {
			rec.Status = usage.StatusFailed
			if errors.Is(err, billing.ErrInsufficient) {
				rec.ErrorCode = "insufficient_balance"
				openAIError(c, http.StatusPaymentRequired, "insufficient_balance",
					"积分不足,请前往「账单与充值」充值后再试")
				return true
			}
			rec.ErrorCode = "billing_error"
			openAIError(c, http.StatusInternalServerError, "billing_error", "计费异常:"+err.Error())
			return true
		}
	}
	refunded := false
	refund := func(code string) {
		rec.Status = usage.StatusFailed
		rec.ErrorCode = code
		if refunded || cost == 0 {
			return
		}
		refunded = true
		_ = h.Billing.Refund(context.Background(), ak.UserID, ak.ID, cost, refID, "image refund")
	}

	ir := imageAdapterRequest(m, req, refs)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 7*time.Minute)
	defer cancel()

	var lastErr error
	var result *adapter.ImageResult
	var selected *channel.Route
	for _, rt := range routes {
		r, err := imageChannelGenerateWithRetry(ctx, rt, ir, "", 0, nil)
		if err != nil {
			lastErr = err
			if adapter.IsContentModerationError(err) {
				logger.L().Warn("channel image content moderation",
					zap.Uint64("channel_id", rt.Channel.ID),
					zap.String("channel_name", rt.Channel.Name),
					zap.Error(err))
				break
			}
			if isImageChannelUserRequestError(err) {
				logger.L().Warn("channel image user request error",
					zap.Uint64("channel_id", rt.Channel.ID),
					zap.String("channel_name", rt.Channel.Name),
					zap.Error(err))
				break
			}
			_ = h.Channels.Svc().MarkHealth(context.Background(), rt.Channel, false, err.Error())
			logger.L().Warn("channel image fail, try next",
				zap.Uint64("channel_id", rt.Channel.ID),
				zap.String("channel_name", rt.Channel.Name),
				zap.Error(err))
			continue
		}
		result = r
		selected = rt
		break
	}

	if result == nil {
		if shouldFallbackImageChannelToFree(lastErr) && h.Runner != nil && req != nil {
			logger.L().Warn("channel image transient failure, fallback to free account runner",
				zap.String("model", m.Slug),
				zap.Error(lastErr))
			refund(imagepkg.ErrUpstream)
			req.freeFallback = true
			req.freeFallbackDetail = lastErr.Error()
			return false
		}
		failure := imageChannelFailureFromErr(lastErr)
		refund(failure.Code)
		openAIError(c, failure.HTTPStatus, failure.Code, failure.Message)
		return true
	}
	result = limitImageChannelResult(result, req.N)
	_ = h.Channels.Svc().MarkHealth(context.Background(), selected.Channel, true, "")

	// 渠道级倍率叠乘
	channelRatio := selected.Channel.Ratio
	if channelRatio <= 0 {
		channelRatio = 1.0
	}
	finalCost := billing.ComputeImageCost(m, actualCount(result), ratio*channelRatio)

	data := make([]ImageGenData, 0, actualCount(result))
	for _, u := range result.URLs {
		data = append(data, ImageGenData{URL: u})
	}
	// base64 → data: URL,浏览器直接可渲染。
	// (若后续需要 b64_json 直返,ImageGenData 补一个 B64 字段即可。)
	for _, b := range result.B64s {
		data = append(data, ImageGenData{URL: "data:image/png;base64," + b})
	}

	if finalCost > 0 {
		if err := h.Billing.Settle(context.Background(), ak.UserID, ak.ID, cost, finalCost, refID, "image settle"); err != nil {
			logger.L().Error("billing settle image channel", zap.Error(err), zap.String("ref", refID))
		}
	}
	_ = h.Keys.DAO().TouchUsage(context.Background(), ak.ID, c.ClientIP(), finalCost)

	rec.Status = usage.StatusSuccess
	rec.ModelID = m.ID
	rec.CreditCost = finalCost
	rec.ImageCount = actualCount(result)

	c.JSON(http.StatusOK, ImageGenResponse{
		Created: time.Now().Unix(),
		Data:    data,
	})
	return true
}

func (h *ImagesHandler) dispatchImageToChannelAsync(c *gin.Context,
	ak *apikey.APIKey, m *modelpkg.Model, req *ImageGenRequest,
	rec *usage.Log, ratio float64, refs []imagepkg.ReferenceImage,
) (bool, bool) {
	if h.Channels == nil {
		return false, false
	}
	routes, err := h.Channels.Resolve(c.Request.Context(), m.Slug, channel.ModalityImage)
	if err != nil {
		if errors.Is(err, channel.ErrNoRoute) {
			return false, false
		}
		logger.L().Warn("channel resolve async image", zap.Error(err), zap.String("model", m.Slug))
		return false, false
	}
	if len(routes) == 0 {
		return false, false
	}
	if h.DAO == nil {
		rec.Status = usage.StatusFailed
		rec.ErrorCode = "not_configured"
		openAIError(c, http.StatusInternalServerError, "not_configured", "图片任务存储未初始化,请联系管理员")
		return true, false
	}

	refID := uuid.NewString()
	rec.RequestID = refID
	cost := billing.ComputeImageCost(m, req.N, ratio)
	if cost > 0 {
		if err := h.Billing.PreDeduct(c.Request.Context(), ak.UserID, ak.ID, cost, refID, "image channel async prepay"); err != nil {
			rec.Status = usage.StatusFailed
			if errors.Is(err, billing.ErrInsufficient) {
				rec.ErrorCode = "insufficient_balance"
				openAIError(c, http.StatusPaymentRequired, "insufficient_balance",
					"积分不足,请前往「账单与充值」充值后再试")
				return true, false
			}
			rec.ErrorCode = "billing_error"
			openAIError(c, http.StatusInternalServerError, "billing_error", "计费异常:"+err.Error())
			return true, false
		}
	}

	taskID := imagepkg.GenerateTaskID()
	task := &imagepkg.Task{
		TaskID:          taskID,
		UserID:          ak.UserID,
		KeyID:           ak.ID,
		ModelID:         m.ID,
		Prompt:          req.Prompt,
		N:               req.N,
		Size:            req.Size,
		Status:          imagepkg.StatusDispatched,
		EstimatedCredit: cost,
	}
	downstreamUserInfoForTask(c, ak, req.User).applyToTask(task)
	if err := h.DAO.Create(c.Request.Context(), task); err != nil {
		rec.Status = usage.StatusFailed
		rec.ErrorCode = "billing_error"
		if cost > 0 {
			_ = h.Billing.Refund(context.Background(), ak.UserID, ak.ID, cost, refID, "image channel async create refund")
		}
		openAIError(c, http.StatusInternalServerError, "internal_error", "创建任务失败:"+err.Error())
		return true, false
	}

	h.runImageChannelTaskAsync(imageChannelAsyncJob{
		TaskID:     taskID,
		UserID:     ak.UserID,
		KeyID:      ak.ID,
		ModelID:    m.ID,
		Model:      m,
		Ratio:      ratio,
		Routes:     routes,
		Request:    imageAdapterRequest(m, req, refs),
		References: refs,
		Cost:       cost,
		RefID:      refID,
		IP:         c.ClientIP(),
		UA:         c.Request.UserAgent(),
	})
	writeAsyncImageSubmit(c, taskID)
	return true, true
}

type imageChannelAsyncJob struct {
	TaskID     string
	UserID     uint64
	KeyID      uint64
	ModelID    uint64
	Model      *modelpkg.Model
	Ratio      float64
	Routes     []*channel.Route
	Request    *adapter.ImageRequest
	References []imagepkg.ReferenceImage
	Cost       int64
	RefID      string
	IP         string
	UA         string
}

func (h *ImagesHandler) runImageChannelTaskAsync(job imageChannelAsyncJob) {
	go func() {
		startAt := time.Now()
		rec := &usage.Log{
			UserID:    job.UserID,
			KeyID:     job.KeyID,
			ModelID:   job.ModelID,
			RequestID: job.RefID,
			Type:      usage.TypeImage,
			IP:        job.IP,
			UA:        job.UA,
		}
		defer func() {
			rec.DurationMs = int(time.Since(startAt).Milliseconds())
			if rec.Status == "" {
				rec.Status = usage.StatusFailed
			}
			if h.Usage != nil {
				h.Usage.Write(rec)
			}
		}()

		if h.DAO != nil {
			_ = h.DAO.MarkRunning(context.Background(), job.TaskID, 0)
		}
		ctx, cancel := context.WithTimeout(context.Background(), imageChannelAsyncTimeout(len(job.References) > 0))
		defer cancel()

		var lastErr error
		var result *adapter.ImageResult
		var selected *channel.Route
		for _, rt := range job.Routes {
			r, err := imageChannelGenerateWithRetry(ctx, rt, job.Request, job.TaskID, imageChannelAsyncPerAttemptTimeout(len(job.References) > 0), nil)
			if err != nil {
				lastErr = err
				if adapter.IsContentModerationError(err) {
					logger.L().Warn("channel async image content moderation",
						zap.Uint64("channel_id", rt.Channel.ID),
						zap.String("channel_name", rt.Channel.Name),
						zap.String("task_id", job.TaskID),
						zap.Error(err))
					break
				}
				if isImageChannelUserRequestError(err) {
					logger.L().Warn("channel async image user request error",
						zap.Uint64("channel_id", rt.Channel.ID),
						zap.String("channel_name", rt.Channel.Name),
						zap.String("task_id", job.TaskID),
						zap.Error(err))
					break
				}
				_ = h.Channels.Svc().MarkHealth(context.Background(), rt.Channel, false, err.Error())
				logger.L().Warn("channel async image fail, try next",
					zap.Uint64("channel_id", rt.Channel.ID),
					zap.String("channel_name", rt.Channel.Name),
					zap.String("task_id", job.TaskID),
					zap.Error(err))
				continue
			}
			result = r
			selected = rt
			break
		}

		if result == nil {
			if shouldFallbackImageChannelToFree(lastErr) && h.Runner != nil {
				logger.L().Warn("channel async image transient failure, fallback to free account runner",
					zap.String("task_id", job.TaskID),
					zap.Error(lastErr))
				fallbackOpt := imageChannelFreeFallbackRunOptions(job)
				fallbackCtx, cancelFallback := context.WithTimeout(context.Background(), asyncImageTaskTimeout(fallbackOpt.MaxAttempts, len(job.References) > 0))
				fallback := h.Runner.Run(fallbackCtx, fallbackOpt)
				cancelFallback()
				rec.AccountID = fallback.AccountID
				if fallback.Status == imagepkg.StatusSuccess {
					if job.Cost > 0 && h.Billing != nil {
						if err := h.Billing.Settle(context.Background(), job.UserID, job.KeyID, job.Cost, job.Cost, job.RefID, "image channel async free fallback settle"); err != nil {
							logger.L().Error("billing settle async free fallback image", zap.Error(err), zap.String("ref", job.RefID))
						}
					}
					if h.Keys != nil && h.Keys.DAO() != nil {
						_ = h.Keys.DAO().TouchUsage(context.Background(), job.KeyID, job.IP, job.Cost)
					}
					if h.DAO != nil {
						_ = h.DAO.UpdateCost(context.Background(), job.TaskID, job.Cost)
					}
					rec.Status = usage.StatusSuccess
					rec.CreditCost = job.Cost
					return
				}
				rec.Status = usage.StatusFailed
				rec.ErrorCode = ifEmpty(fallback.ErrorCode, imagepkg.ErrUpstream)
				if job.Cost > 0 && h.Billing != nil {
					_ = h.Billing.Refund(context.Background(), job.UserID, job.KeyID, job.Cost, job.RefID, "image channel async free fallback refund")
				}
				return
			}
			failure := imageChannelFailureFromErr(lastErr)
			rec.Status = usage.StatusFailed
			rec.ErrorCode = failure.Code
			if h.DAO != nil {
				_ = h.DAO.MarkFailedDetail(context.Background(), job.TaskID, failure.Code, failure.Detail)
			}
			if job.Cost > 0 && h.Billing != nil {
				_ = h.Billing.Refund(context.Background(), job.UserID, job.KeyID, job.Cost, job.RefID, "image channel async refund")
			}
			return
		}
		result = limitImageChannelResult(result, job.Request.N)
		_ = h.Channels.Svc().MarkHealth(context.Background(), selected.Channel, true, "")

		channelRatio := selected.Channel.Ratio
		if channelRatio <= 0 {
			channelRatio = 1.0
		}
		finalCost := billing.ComputeImageCost(job.Model, actualCount(result), job.Ratio*channelRatio)
		if job.Cost > 0 && h.Billing != nil {
			if err := h.Billing.Settle(context.Background(), job.UserID, job.KeyID, job.Cost, finalCost, job.RefID, "image channel async settle"); err != nil {
				logger.L().Error("billing settle async channel image", zap.Error(err), zap.String("ref", job.RefID))
			}
		}
		if h.Keys != nil && h.Keys.DAO() != nil {
			_ = h.Keys.DAO().TouchUsage(context.Background(), job.KeyID, job.IP, finalCost)
		}
		if h.DAO != nil {
			_ = h.DAO.MarkSuccess(context.Background(), job.TaskID, "", nil, imageChannelResultURLs(result), finalCost)
		}
		rec.Status = usage.StatusSuccess
		rec.CreditCost = finalCost
		rec.ImageCount = actualCount(result)
	}()
}

func (h *ImagesHandler) dispatchChatImageToChannel(c *gin.Context,
	ak *apikey.APIKey, m *modelpkg.Model, req *ImageGenRequest,
	rec *usage.Log, ratio float64, startAt time.Time,
) bool {
	if h.Channels == nil {
		return false
	}
	routes, err := h.Channels.Resolve(c.Request.Context(), m.Slug, channel.ModalityImage)
	if err != nil {
		if errors.Is(err, channel.ErrNoRoute) {
			return false
		}
		logger.L().Warn("channel resolve chat image", zap.Error(err), zap.String("model", m.Slug))
		return false
	}
	if len(routes) == 0 {
		return false
	}

	refID := uuid.NewString()
	rec.RequestID = refID

	cost := billing.ComputeImageCost(m, req.N, ratio)
	if cost > 0 {
		if err := h.Billing.PreDeduct(c.Request.Context(), ak.UserID, ak.ID, cost, refID, "chat->image channel prepay"); err != nil {
			rec.Status = usage.StatusFailed
			if errors.Is(err, billing.ErrInsufficient) {
				rec.ErrorCode = "insufficient_balance"
				openAIError(c, http.StatusPaymentRequired, "insufficient_balance",
					"积分不足,请前往「账单与充值」充值后再试")
				return true
			}
			rec.ErrorCode = "billing_error"
			openAIError(c, http.StatusInternalServerError, "billing_error", "计费异常:"+err.Error())
			return true
		}
	}
	refunded := false
	refund := func(code string) {
		rec.Status = usage.StatusFailed
		rec.ErrorCode = code
		if refunded || cost == 0 {
			return
		}
		refunded = true
		_ = h.Billing.Refund(context.Background(), ak.UserID, ak.ID, cost, refID, "chat->image channel refund")
	}

	ir := imageAdapterRequest(m, req, nil)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 7*time.Minute)
	defer cancel()

	var lastErr error
	var result *adapter.ImageResult
	var selected *channel.Route
	for _, rt := range routes {
		r, err := imageChannelGenerateWithRetry(ctx, rt, ir, "", 0, nil)
		if err != nil {
			lastErr = err
			if adapter.IsContentModerationError(err) {
				logger.L().Warn("channel chat image content moderation",
					zap.Uint64("channel_id", rt.Channel.ID),
					zap.String("channel_name", rt.Channel.Name),
					zap.Error(err))
				break
			}
			if isImageChannelUserRequestError(err) {
				logger.L().Warn("channel chat image user request error",
					zap.Uint64("channel_id", rt.Channel.ID),
					zap.String("channel_name", rt.Channel.Name),
					zap.Error(err))
				break
			}
			_ = h.Channels.Svc().MarkHealth(context.Background(), rt.Channel, false, err.Error())
			logger.L().Warn("channel chat image fail, try next",
				zap.Uint64("channel_id", rt.Channel.ID),
				zap.String("channel_name", rt.Channel.Name),
				zap.Error(err))
			continue
		}
		result = r
		selected = rt
		break
	}

	if result == nil {
		if shouldFallbackImageChannelToFree(lastErr) && h.Runner != nil && req != nil {
			logger.L().Warn("channel chat image transient failure, fallback to free account runner",
				zap.String("model", m.Slug),
				zap.Error(lastErr))
			refund(imagepkg.ErrUpstream)
			req.freeFallback = true
			req.freeFallbackDetail = lastErr.Error()
			return false
		}
		failure := imageChannelFailureFromErr(lastErr)
		refund(failure.Code)
		openAIError(c, failure.HTTPStatus, failure.Code, failure.Message)
		return true
	}
	result = limitImageChannelResult(result, req.N)
	_ = h.Channels.Svc().MarkHealth(context.Background(), selected.Channel, true, "")

	channelRatio := selected.Channel.Ratio
	if channelRatio <= 0 {
		channelRatio = 1.0
	}
	finalCost := billing.ComputeImageCost(m, actualCount(result), ratio*channelRatio)
	if finalCost > 0 {
		if err := h.Billing.Settle(context.Background(), ak.UserID, ak.ID, cost, finalCost, refID, "chat->image channel settle"); err != nil {
			logger.L().Error("billing settle chat image channel", zap.Error(err), zap.String("ref", refID))
		}
	}
	_ = h.Keys.DAO().TouchUsage(context.Background(), ak.ID, c.ClientIP(), finalCost)

	rec.Status = usage.StatusSuccess
	rec.ModelID = m.ID
	rec.CreditCost = finalCost
	rec.DurationMs = int(time.Since(startAt).Milliseconds())
	rec.ImageCount = actualCount(result)

	c.JSON(http.StatusOK, imageChannelChatResponse(m.Slug, result))
	return true
}

func imageAdapterRequest(m *modelpkg.Model, req *ImageGenRequest, refs []imagepkg.ReferenceImage) *adapter.ImageRequest {
	model := ""
	if m != nil {
		model = m.Slug
	}
	return &adapter.ImageRequest{
		Model:             model,
		Prompt:            req.Prompt,
		N:                 req.N,
		Size:              req.Size,
		AspectRatio:       req.AspectRatio,
		Resolution:        req.Resolution,
		Images:            referenceImageDataURLs(refs),
		Quality:           req.Quality,
		Style:             req.Style,
		Format:            req.ResponseFormat,
		OutputFormat:      req.OutputFormat,
		OutputCompression: req.OutputCompression,
		Background:        req.Background,
		Moderation:        req.Moderation,
	}
}

func referenceImageDataURLs(refs []imagepkg.ReferenceImage) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if len(ref.Data) == 0 {
			continue
		}
		contentType := http.DetectContentType(ref.Data)
		if i := strings.Index(contentType, ";"); i >= 0 {
			contentType = strings.TrimSpace(contentType[:i])
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		out = append(out, "data:"+contentType+";base64,"+base64.StdEncoding.EncodeToString(ref.Data))
	}
	return out
}

func imageChannelChatResponse(model string, result *adapter.ImageResult) ChatCompletionResponse {
	var sb strings.Builder
	if result != nil {
		for _, u := range result.URLs {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString("![generated](")
			sb.WriteString(u)
			sb.WriteString(")")
		}
		for _, b := range result.B64s {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString("![generated](data:image/png;base64,")
			sb.WriteString(b)
			sb.WriteString(")")
		}
	}
	return ChatCompletionResponse{
		ID:      "chatcmpl-" + uuid.NewString(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ChatCompletionChoice{{
			Index: 0,
			Message: chatMsg{
				Role:    "assistant",
				Content: sb.String(),
			},
			FinishReason: "stop",
		}},
		Usage: ChatCompletionUsage{},
	}
}

func imageChannelResultURLs(result *adapter.ImageResult) []string {
	if result == nil {
		return nil
	}
	urls := make([]string, 0, len(result.URLs)+len(result.B64s))
	urls = append(urls, result.URLs...)
	for _, b := range result.B64s {
		if strings.TrimSpace(b) == "" {
			continue
		}
		urls = append(urls, "data:image/png;base64,"+b)
	}
	return urls
}

func limitImageChannelResult(result *adapter.ImageResult, requested int) *adapter.ImageResult {
	if result == nil {
		return nil
	}
	if requested <= 0 {
		requested = 1
	}
	limited := *result
	remaining := requested
	if len(result.URLs) > 0 {
		if len(result.URLs) >= remaining {
			limited.URLs = append([]string(nil), result.URLs[:remaining]...)
			limited.B64s = nil
			return &limited
		}
		limited.URLs = append([]string(nil), result.URLs...)
		remaining -= len(limited.URLs)
	} else {
		limited.URLs = nil
	}
	if len(result.B64s) > remaining {
		limited.B64s = append([]string(nil), result.B64s[:remaining]...)
	} else if len(result.B64s) > 0 {
		limited.B64s = append([]string(nil), result.B64s...)
	} else {
		limited.B64s = nil
	}
	return &limited
}

func actualCount(r *adapter.ImageResult) int {
	if r == nil {
		return 0
	}
	n := len(r.URLs) + len(r.B64s)
	if n == 0 {
		return 1
	}
	return n
}

func isImageChannelUserRequestError(err error) bool {
	if err == nil || adapter.IsContentModerationError(err) {
		return false
	}
	var upstream *adapter.UpstreamHTTPError
	if !errors.As(err, &upstream) {
		return false
	}
	if upstream.Status != http.StatusBadRequest && upstream.Status != http.StatusRequestEntityTooLarge && upstream.Status != http.StatusUnprocessableEntity {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(upstream.Code))
	errType := strings.ToLower(strings.TrimSpace(upstream.Type))
	msg := strings.ToLower(strings.TrimSpace(upstream.Message + " " + upstream.Body))
	return errType == "image_generation_user_error" ||
		code == "invalid_value" ||
		code == "invalid_request_error" ||
		strings.Contains(msg, "invalid size") ||
		strings.Contains(msg, "minimum pixel budget") ||
		strings.Contains(msg, "requested resolution") ||
		strings.Contains(msg, "unsupported size")
}

func shouldFallbackImageChannelToFree(err error) bool {
	if err == nil || adapter.IsContentModerationError(err) || isImageChannelUserRequestError(err) {
		return false
	}
	var upstream *adapter.UpstreamHTTPError
	if errors.As(err, &upstream) {
		return upstream.Status == http.StatusRequestTimeout || upstream.Status >= http.StatusInternalServerError
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "upstream 502") ||
		strings.Contains(msg, "upstream 500") ||
		strings.Contains(msg, "internal_error") ||
		strings.Contains(msg, "internal_server_error") ||
		strings.Contains(msg, "status 502") ||
		strings.Contains(msg, "bad gateway") ||
		strings.Contains(msg, "stream disconnected before completion") ||
		strings.Contains(msg, "stream error") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "broken pipe")
}

func applyFreeFallbackPlan(opt *imagepkg.RunOptions, enabled bool) {
	if opt == nil || !enabled {
		return
	}
	opt.PreferredPlanType = "free"
	opt.RequirePlanType = true
}

func imageChannelFreeFallbackRunOptions(job imageChannelAsyncJob) imagepkg.RunOptions {
	upstreamModel := ""
	if job.Model != nil {
		upstreamModel = job.Model.UpstreamModelSlug
	}
	prompt := ""
	n := 1
	if job.Request != nil {
		prompt = job.Request.Prompt
		n = job.Request.N
	}
	maxAttempts := 2
	runAttempts, perAttemptTimeout, pollMaxWait, dispatchTimeout := asyncImageRunTuning(maxAttempts, len(job.References) > 0)
	opt := imagepkg.RunOptions{
		TaskID:            job.TaskID,
		UserID:            job.UserID,
		KeyID:             job.KeyID,
		ModelID:           job.ModelID,
		UpstreamModel:     upstreamModel,
		Prompt:            maybeAppendClaritySuffix(prompt),
		N:                 n,
		MaxAttempts:       runAttempts,
		DispatchTimeout:   dispatchTimeout,
		PerAttemptTimeout: perAttemptTimeout,
		PollMaxWait:       pollMaxWait,
		References:        job.References,
	}
	applyFreeFallbackPlan(&opt, true)
	return opt
}

func imageChannelAsyncPerAttemptTimeout(hasReferences bool) time.Duration {
	if hasReferences {
		return 3 * time.Minute
	}
	return 2 * time.Minute
}

func imageChannelAsyncTimeout(hasReferences bool) time.Duration {
	const maxAttempts = 2
	timeout := time.Duration(maxAttempts)*imageChannelAsyncPerAttemptTimeout(hasReferences) + 30*time.Second
	if timeout > 8*time.Minute {
		return 8 * time.Minute
	}
	return timeout
}

type imageChannelFailure struct {
	Code       string
	HTTPStatus int
	Message    string
	Detail     string
}

func imageChannelGenerateWithRetry(ctx context.Context, rt *channel.Route, req *adapter.ImageRequest, taskID string, perAttemptTimeout time.Duration, sleep func(context.Context, time.Duration) error) (*adapter.ImageResult, error) {
	if sleep == nil {
		sleep = sleepWithContext
	}
	const maxAttempts = 2

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attemptCtx := ctx
		cancel := func() {}
		if perAttemptTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, perAttemptTimeout)
		}
		result, err := rt.Adapter.ImageGenerate(attemptCtx, rt.UpstreamModel, req)
		cancel()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempt >= maxAttempts || !isRetryableImageChannelError(err) {
			return nil, err
		}
		logger.L().Warn("channel image transient fail, retry same channel",
			zap.Uint64("channel_id", rt.Channel.ID),
			zap.String("channel_name", rt.Channel.Name),
			zap.String("task_id", taskID),
			zap.Int("attempt", attempt),
			zap.Error(err))
		if err := sleep(ctx, imageChannelRetryDelay(attempt)); err != nil {
			return nil, lastErr
		}
	}
	return nil, lastErr
}

func isRetryableImageChannelError(err error) bool {
	if err == nil || adapter.IsContentModerationError(err) {
		return false
	}
	var upstream *adapter.UpstreamHTTPError
	if errors.As(err, &upstream) {
		if upstream.Status == http.StatusRequestTimeout || upstream.Status >= http.StatusInternalServerError {
			return true
		}
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "stream disconnected before completion") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "broken pipe")
}

func imageChannelRetryDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	return time.Duration(attempt) * 500 * time.Millisecond
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func imageChannelFailureFromErr(err error) imageChannelFailure {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	if adapter.IsContentModerationError(err) {
		return imageChannelFailure{
			Code:       imagepkg.ErrContentModeration,
			HTTPStatus: http.StatusBadRequest,
			Message:    localizeImageErr(imagepkg.ErrContentModeration, detail),
			Detail:     detail,
		}
	}
	if isImageChannelUserRequestError(err) {
		msg := "图片请求参数不被上游接受"
		if detail != "" {
			msg += ":" + detail
		}
		return imageChannelFailure{
			Code:       "invalid_request_error",
			HTTPStatus: http.StatusBadRequest,
			Message:    msg,
			Detail:     detail,
		}
	}
	msg := "所有上游渠道均不可用"
	if detail != "" {
		msg += ":" + detail
	}
	return imageChannelFailure{
		Code:       "upstream_error",
		HTTPStatus: http.StatusBadGateway,
		Message:    msg,
		Detail:     detail,
	}
}
