package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/apikey"
	"github.com/432539/gpt2api/internal/billing"
	"github.com/432539/gpt2api/internal/image"
	modelpkg "github.com/432539/gpt2api/internal/model"
	"github.com/432539/gpt2api/internal/upstream/chatgpt"
	"github.com/432539/gpt2api/internal/usage"
	"github.com/432539/gpt2api/pkg/logger"
	"github.com/432539/gpt2api/pkg/oaierr"
)

// 单张参考图的硬上限(字节)。chatgpt.com 的 /backend-api/files 实测上限大致 20MB。
const maxReferenceImageBytes = 20 * 1024 * 1024

// 同一次请求最多携带的参考图数量。
const maxReferenceImages = 4

const (
	referenceFetchTimeout    = 20 * time.Second
	referenceFetchMaxAttempt = 2
)

const (
	asyncImageNoReferenceTimeout = 8 * time.Minute
	asyncImageReferenceTimeout   = 8*time.Minute + 30*time.Second
)

// chatMsg 是 OpenAI chat message 的本地别名,便于 handleChatAsImage 内部表达。
type chatMsg = chatgpt.ChatMessage

type stringList []string

func (s *stringList) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = nil
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*s = list
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			*s = nil
			return nil
		}
		*s = []string{single}
		return nil
	}
	var obj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &obj); err == nil && strings.TrimSpace(obj.URL) != "" {
		*s = []string{obj.URL}
		return nil
	}
	var objs []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &objs); err == nil {
		out := make([]string, 0, len(objs))
		for _, item := range objs {
			if strings.TrimSpace(item.URL) != "" {
				out = append(out, item.URL)
			}
		}
		*s = out
		return nil
	}
	return fmt.Errorf("expected string, string array, url object, or url object array")
}

// ImagesHandler 挂载在 /v1/images/* 下的处理器。
//
// 复用 Handler 的依赖(鉴权/模型/计费/限流/usage)加上专属的 image.Runner 和 DAO。
// 路由:
//
//	POST /v1/images/generations       同步生图(默认)
//	GET  /v1/images/tasks/:id         查询历史任务(按 task_id)
type ImagesHandler struct {
	*Handler
	Runner          *image.Runner
	DAO             *image.DAO
	SuperResolution *image.AliyunSuperResolutionClient
	// ImageAccResolver 可选:代理下载上游图片时用于解出账号 AT/cookies/proxy。
	// 未注入时 /p/img 路径会返回 503。
	ImageAccResolver ImageAccountResolver
}

// ImageGenRequest OpenAI 兼容入参。
//
// 对 reference_images 的扩展:OpenAI 的 /images/generations 规范没有这个字段;
// 这里加一项非标准扩展,便于 Playground / Web UI 发起"图生图"走同一条 generations 路径。
// 每一项可以是:
//   - https:// URL       直接 HTTP GET
//   - data:<mime>;base64,xxxx   dataURL
//   - 纯 base64 字符串            兼容
type ImageGenRequest struct {
	Model             string     `json:"model"`
	Prompt            string     `json:"prompt"`
	N                 int        `json:"n"`
	Size              string     `json:"size"`
	AspectRatio       string     `json:"-"`
	Quality           string     `json:"quality,omitempty"`
	Style             string     `json:"style,omitempty"`
	ResponseFormat    string     `json:"response_format,omitempty"` // url | b64_json(暂仅支持 url)
	OutputFormat      string     `json:"output_format,omitempty"`
	OutputCompression *int       `json:"output_compression,omitempty"`
	Background        string     `json:"background,omitempty"`
	Moderation        string     `json:"moderation,omitempty"`
	Resolution        string     `json:"resolution,omitempty"`
	ImageSize         string     `json:"image_size,omitempty"`
	Scale             string     `json:"scale,omitempty"`
	User              string     `json:"user,omitempty"`
	ReferenceImages   stringList `json:"reference_images,omitempty"` // 非标准扩展,见注释
	Images            stringList `json:"images,omitempty"`
	Image             stringList `json:"image,omitempty"`
	ImageURL          stringList `json:"image_url,omitempty"`
	ImageURLs         stringList `json:"image_urls,omitempty"`
	InputImage        stringList `json:"input_image,omitempty"`
	InputImages       stringList `json:"input_images,omitempty"`
	// Upscale 非标准扩展:控制"本服务对原图做 AI 超分"的目标档位。
	// 可选值:""(原图直出,默认)/ "2k"(长边 2560) / "4k"(长边 3840)。
	// 生效时机:图片代理 URL 首次请求时调用外部超分服务,之后进程内
	// LRU 缓存命中毫秒级返回。仅影响 /v1/images/proxy/... 的出口字节,不改原图。
	Upscale       string `json:"upscale,omitempty"`
	WaitForResult *bool  `json:"wait_for_result,omitempty"` // false=立即返回 task_id,客户端自行轮询

	freeFallback       bool
	freeFallbackDetail string
}

// ImageGenData 单张图响应。
type ImageGenData struct {
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
	FileID        string `json:"file_id,omitempty"` // chatgpt.com 侧原始 id(用于对账)
}

// ImageGenResponse OpenAI 兼容返回。
type ImageGenResponse struct {
	Created int64          `json:"created"`
	Data    []ImageGenData `json:"data"`
	TaskID  string         `json:"task_id,omitempty"`
}

type imageAsyncJob struct {
	TaskID        string
	UserID        uint64
	KeyID         uint64
	ModelID       uint64
	UpstreamModel string
	Prompt        string
	N             int
	MaxAttempts   int
	References    []image.ReferenceImage
	Cost          int64
	RefID         string
	IP            string
	UA            string
}

func (h *ImagesHandler) runImageTaskAsync(job imageAsyncJob) {
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

		ctx, cancel := context.WithTimeout(context.Background(), asyncImageTaskTimeout(job.MaxAttempts, len(job.References) > 0))
		defer cancel()
		maxAttempts, perAttemptTimeout, pollMaxWait, dispatchTimeout := asyncImageRunTuning(job.MaxAttempts, len(job.References) > 0)

		res := h.Runner.Run(ctx, image.RunOptions{
			TaskID:            job.TaskID,
			UserID:            job.UserID,
			KeyID:             job.KeyID,
			ModelID:           job.ModelID,
			UpstreamModel:     job.UpstreamModel,
			Prompt:            job.Prompt,
			N:                 job.N,
			MaxAttempts:       maxAttempts,
			DispatchTimeout:   dispatchTimeout,
			PerAttemptTimeout: perAttemptTimeout,
			PollMaxWait:       pollMaxWait,
			References:        job.References,
		})
		rec.AccountID = res.AccountID

		if res.Status != image.StatusSuccess {
			rec.Status = usage.StatusFailed
			rec.ErrorCode = ifEmpty(res.ErrorCode, "upstream_error")
			if job.Cost > 0 && h.Billing != nil {
				_ = h.Billing.Refund(context.Background(), job.UserID, job.KeyID, job.Cost, job.RefID, "image async refund")
			}
			return
		}

		if job.Cost > 0 && h.Billing != nil {
			if err := h.Billing.Settle(context.Background(), job.UserID, job.KeyID, job.Cost, job.Cost, job.RefID, "image async settle"); err != nil {
				logger.L().Error("billing settle async image", zap.Error(err), zap.String("ref", job.RefID))
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
	}()
}

func asyncImageTaskTimeout(maxAttempts int, hasReferences bool) time.Duration {
	attempts, perAttemptTimeout, _, _ := asyncImageRunTuning(maxAttempts, hasReferences)
	timeout := time.Duration(attempts)*perAttemptTimeout + 30*time.Second
	if hasReferences {
		if timeout != asyncImageReferenceTimeout {
			return asyncImageReferenceTimeout
		}
		return timeout
	}
	if timeout > asyncImageNoReferenceTimeout {
		return asyncImageNoReferenceTimeout
	}
	return timeout
}

func asyncImageRunTuning(maxAttempts int, hasReferences bool) (int, time.Duration, time.Duration, time.Duration) {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if hasReferences {
		if maxAttempts > 2 {
			maxAttempts = 2
		}
		return maxAttempts, 3 * time.Minute, 90 * time.Second, 15 * time.Second
	}
	if maxAttempts < 5 {
		maxAttempts = 5
	}
	if maxAttempts > 5 {
		maxAttempts = 5
	}
	return maxAttempts, 90 * time.Second, 60 * time.Second, 10 * time.Second
}

// ImageGenerations POST /v1/images/generations。
func (h *ImagesHandler) ImageGenerations(c *gin.Context) {
	startAt := time.Now()
	ak, ok := apikey.FromCtx(c)
	if !ok {
		openAIError(c, http.StatusUnauthorized, "missing_api_key", "缺少 API Key")
		return
	}

	var req ImageGenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "请求参数错误:"+err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "prompt 不能为空")
		return
	}
	if req.Model == "" {
		req.Model = "gpt-image-2"
	}
	if req.N <= 0 {
		req.N = 1
	}
	if req.N > 4 {
		req.N = 4 // 目前 IMG2 终稿单轮稳定产出 1-4 张,保守上限
	}
	if req.Size == "" {
		req.Size = "1024x1024"
	}
	explicitUpscale := requestedUpscaleFromOptions(req.Upscale, req.Resolution, req.ImageSize, req.Scale, req.Quality)
	req.Upscale = explicitUpscale
	logImageRequestOptions("image generation options", &req, explicitUpscale)

	refID := uuid.NewString()
	rec := &usage.Log{
		UserID:    ak.UserID,
		KeyID:     ak.ID,
		RequestID: refID,
		Type:      usage.TypeImage,
		IP:        c.ClientIP(),
		UA:        c.Request.UserAgent(),
	}
	writeUsageOnReturn := true
	defer func() {
		if !writeUsageOnReturn {
			return
		}
		rec.DurationMs = int(time.Since(startAt).Milliseconds())
		if rec.Status == "" {
			rec.Status = usage.StatusFailed
		}
		if h.Usage != nil {
			h.Usage.Write(rec)
		}
	}()
	fail := func(code string) { rec.Status = usage.StatusFailed; rec.ErrorCode = code }

	// 1) 模型白名单
	if !ak.ModelAllowed(req.Model) {
		fail("model_not_allowed")
		openAIError(c, http.StatusForbidden, "model_not_allowed",
			fmt.Sprintf("当前 API Key 无权调用模型 %q", req.Model))
		return
	}
	m, err := h.Models.BySlug(c.Request.Context(), req.Model)
	if err != nil || m == nil || !m.Enabled {
		fail("model_not_found")
		openAIError(c, http.StatusBadRequest, "model_not_found",
			fmt.Sprintf("模型 %q 不存在或已下架", req.Model))
		return
	}
	if m.Type != modelpkg.TypeImage {
		fail("model_type_mismatch")
		openAIError(c, http.StatusBadRequest, "model_type_mismatch",
			fmt.Sprintf("模型 %q 不是图像模型,不能用于 /v1/images/generations", req.Model))
		return
	}
	rec.ModelID = m.ID

	// 2) 分组倍率 + RPM 限流(图像不走 TPM)
	ratio := 1.0
	rpmCap := ak.RPM
	if h.Groups != nil {
		if g, err := h.Groups.OfUser(c.Request.Context(), ak.UserID); err == nil && g != nil {
			ratio = g.Ratio
			if rpmCap == 0 {
				rpmCap = g.RPMLimit
			}
		}
	}
	if h.Limiter != nil {
		if ok, _, err := h.Limiter.AllowRPM(c.Request.Context(), ak.ID, rpmCap); err == nil && !ok {
			fail("rate_limit_rpm")
			openAIError(c, http.StatusTooManyRequests, "rate_limit_rpm",
				"触发每分钟请求数限制 (RPM),请稍后再试")
			return
		}
	}

	// 3) 解析 reference_images(图生图 / 图像编辑入口都走到这里)。
	// 必须在落任务前完成,否则参数错误会留下无人执行的 dispatched 任务。
	referenceInputs := req.referenceInputs()
	refs, err := decodeReferenceInputs(c.Request.Context(), referenceInputs)
	if err != nil {
		fail("invalid_request_error")
		openAIError(c, http.StatusBadRequest, "invalid_reference_image", "参考图解析失败:"+err.Error())
		return
	}

	// 若本地模型配置了外置渠道(OpenAI DALL·E / Gemini imagen / Codex image 等),优先走渠道。
	waitForResult := shouldWaitForImageResult(c, req)
	if h.Channels != nil {
		channelReq := imageRequestForChannel(&req, explicitUpscale)
		if !waitForResult {
			if handled, submitted := h.dispatchImageToChannelAsync(c, ak, m, channelReq, rec, ratio, refs); handled {
				if submitted {
					writeUsageOnReturn = false
				}
				return
			}
		} else if handled := h.dispatchImageToChannel(c, ak, m, channelReq, rec, ratio, refs); handled {
			return
		}
		if channelReq.freeFallback {
			req.freeFallback = true
			req.freeFallbackDetail = channelReq.freeFallbackDetail
		}
	}
	req.Upscale = normalizeImageUpscale(req.Size, explicitUpscale)

	// 4) 预扣(图像按定价,est = actual)
	cost := billing.ComputeImageCost(m, req.N, ratio)
	if cost > 0 {
		if err := h.Billing.PreDeduct(c.Request.Context(), ak.UserID, ak.ID, cost, refID, "image prepay"); err != nil {
			if errors.Is(err, billing.ErrInsufficient) {
				fail("insufficient_balance")
				openAIError(c, http.StatusPaymentRequired, "insufficient_balance",
					"积分不足,请前往「账单与充值」充值后再试")
				return
			}
			fail("billing_error")
			openAIError(c, http.StatusInternalServerError, "billing_error", "计费异常:"+err.Error())
			return
		}
	}
	refunded := false
	refund := func(code string) {
		fail(code)
		if refunded || cost == 0 {
			return
		}
		refunded = true
		_ = h.Billing.Refund(context.Background(), ak.UserID, ak.ID, cost, refID, "image refund")
	}

	// 5) 落任务
	taskID := image.GenerateTaskID()
	downstreamUser := downstreamUserInfoForTask(c, ak, req.User)
	task := &image.Task{
		TaskID:          taskID,
		UserID:          ak.UserID,
		KeyID:           ak.ID,
		ModelID:         m.ID,
		Prompt:          req.Prompt,
		N:               req.N,
		Size:            req.Size,
		Upscale:         req.Upscale,
		Status:          image.StatusDispatched,
		EstimatedCredit: cost,
	}
	downstreamUser.applyToTask(task)
	if h.DAO != nil {
		if err := h.DAO.Create(c.Request.Context(), task); err != nil {
			refund("billing_error")
			openAIError(c, http.StatusInternalServerError, "internal_error", "创建任务失败:"+err.Error())
			return
		}
	}

	// 6) 执行(同步阻塞)
	//
	// 单请求硬上限 7 分钟:Runner 默认 per-attempt 6 分钟
	// (SSE ~60s + PollMaxWait 300s + 缓冲),外层再留 1 分钟
	// 给账号调度 + 签名 URL 换取等周边耗时。IMG2 已正式上线,不再做 preview_only 重试。
	runCtx, cancel := context.WithTimeout(c.Request.Context(), 7*time.Minute)
	defer cancel()

	// 参考图上传链路容易遇到 Azure SAS 端点瞬态 EOF/超时,保留 2 次尝试兜底。
	maxAttempts := 2
	if !waitForResult {
		writeUsageOnReturn = false
		h.runImageTaskAsync(imageAsyncJob{
			TaskID:        taskID,
			UserID:        ak.UserID,
			KeyID:         ak.ID,
			ModelID:       m.ID,
			UpstreamModel: m.UpstreamModelSlug,
			Prompt:        maybeAppendClaritySuffix(req.Prompt),
			N:             req.N,
			MaxAttempts:   maxAttempts,
			References:    refs,
			Cost:          cost,
			RefID:         refID,
			IP:            c.ClientIP(),
			UA:            c.Request.UserAgent(),
		})
		writeAsyncImageSubmit(c, taskID)
		return
	}

	runAttempts, perAttemptTimeout, pollMaxWait, dispatchTimeout := asyncImageRunTuning(maxAttempts, len(refs) > 0)
	runOptions := image.RunOptions{
		TaskID:            taskID,
		UserID:            ak.UserID,
		KeyID:             ak.ID,
		ModelID:           m.ID,
		UpstreamModel:     m.UpstreamModelSlug,
		Prompt:            maybeAppendClaritySuffix(req.Prompt),
		N:                 req.N,
		MaxAttempts:       runAttempts,
		DispatchTimeout:   dispatchTimeout,
		PerAttemptTimeout: perAttemptTimeout,
		PollMaxWait:       pollMaxWait,
		References:        refs,
	}
	applyFreeFallbackPlan(&runOptions, req.freeFallback)
	res := h.Runner.Run(runCtx, runOptions)
	rec.AccountID = res.AccountID

	if res.Status != image.StatusSuccess {
		refund(ifEmpty(res.ErrorCode, "upstream_error"))
		httpStatus := http.StatusBadGateway
		if res.ErrorCode == image.ErrNoAccount {
			httpStatus = http.StatusServiceUnavailable
		}
		if res.ErrorCode == image.ErrRateLimited {
			httpStatus = http.StatusServiceUnavailable
		}
		openAIError(c, httpStatus, ifEmpty(res.ErrorCode, "upstream_error"),
			localizeImageErr(res.ErrorCode, res.ErrorMessage))
		return
	}

	// 6) 结算
	if cost > 0 {
		if err := h.Billing.Settle(context.Background(), ak.UserID, ak.ID, cost, cost, refID, "image settle"); err != nil {
			logger.L().Error("billing settle image", zap.Error(err), zap.String("ref", refID))
		}
	}
	_ = h.Keys.DAO().TouchUsage(context.Background(), ak.ID, c.ClientIP(), cost)

	// 7) usage
	rec.Status = usage.StatusSuccess
	rec.CreditCost = cost
	rec.ImageCount = imageCountFromSignedURLs(res.SignedURLs, req.N)

	// 8) DAO 回写 credit_cost(Runner 已经 MarkSuccess,这里只补 credit_cost)
	if h.DAO != nil {
		_ = h.DAO.UpdateCost(c.Request.Context(), taskID, cost)
	}

	// 9) 响应:URL 统一走自家代理,防止 chatgpt.com estuary/content 防盗链
	out := ImageGenResponse{
		Created: time.Now().Unix(),
		TaskID:  taskID,
		Data:    make([]ImageGenData, 0, len(res.SignedURLs)),
	}
	for i := range res.SignedURLs {
		d := ImageGenData{URL: imageProxyURLForRequest(c, taskID, i)}
		if i < len(res.FileIDs) {
			d.FileID = image.PublicFileID(res.FileIDs[i])
		}
		out.Data = append(out.Data, d)
	}
	c.JSON(http.StatusOK, out)
}

// ImageTask GET /v1/images/tasks/:id。
func (h *ImagesHandler) ImageTask(c *gin.Context) {
	ak, ok := apikey.FromCtx(c)
	if !ok {
		openAIError(c, http.StatusUnauthorized, "missing_api_key", "缺少 API Key")
		return
	}
	id := c.Param("id")
	if id == "" {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "task id 不能为空")
		return
	}
	if h.DAO == nil {
		openAIError(c, http.StatusInternalServerError, "not_configured", "图片任务存储未初始化,请联系管理员")
		return
	}
	t, err := h.DAO.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, image.ErrNotFound) {
			openAIError(c, http.StatusNotFound, "not_found", "任务不存在")
			return
		}
		openAIError(c, http.StatusInternalServerError, "internal_error", "查询任务失败:"+err.Error())
		return
	}
	if t.UserID != ak.UserID {
		openAIError(c, http.StatusNotFound, "not_found", "任务不存在")
		return
	}

	c.JSON(http.StatusOK, buildImageTaskPayload(t, c))
}

// ImageTaskCompat GET /v1/tasks/:id。
//
// 这是给下游任务型网关的 OpenAI/Sora 风格兼容响应。保留原
// /v1/images/tasks/:id 的历史响应不变,避免影响已接入客户端。
func (h *ImagesHandler) ImageTaskCompat(c *gin.Context) {
	ak, ok := apikey.FromCtx(c)
	if !ok {
		openAIError(c, http.StatusUnauthorized, "missing_api_key", "缺少 API Key")
		return
	}
	id := c.Param("id")
	if id == "" {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "task id 不能为空")
		return
	}
	if h.DAO == nil {
		openAIError(c, http.StatusInternalServerError, "not_configured", "图片任务存储未初始化,请联系管理员")
		return
	}
	t, err := h.DAO.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, image.ErrNotFound) {
			openAIError(c, http.StatusNotFound, "not_found", "任务不存在")
			return
		}
		openAIError(c, http.StatusInternalServerError, "internal_error", "查询任务失败:"+err.Error())
		return
	}
	if t.UserID != ak.UserID {
		openAIError(c, http.StatusNotFound, "not_found", "任务不存在")
		return
	}

	c.JSON(http.StatusOK, buildImageTaskCompatPayload(t, c))
}

// handleChatAsImage 是 /v1/chat/completions 发现 model.type=image 时的转派点。
// 行为:
//   - 取最后一条 user message 作为 prompt
//   - 走完整图像链路(同 /v1/images/generations)
//   - 以 assistant message(含 markdown 图片链接)的 OpenAI chat 响应返回
//
// 这样前端只要调用一个端点 /v1/chat/completions,切换 model=gpt-image-2 就能出图。
func (h *ImagesHandler) handleChatAsImage(c *gin.Context, rec *usage.Log, ak *apikey.APIKey,
	m *modelpkg.Model, req *ChatCompletionsRequest, startAt time.Time) {
	rec.ModelID = m.ID
	rec.Type = usage.TypeImage

	prompt := extractLastUserPrompt(req.Messages)
	if strings.TrimSpace(prompt) == "" {
		rec.Status = usage.StatusFailed
		rec.ErrorCode = "invalid_request_error"
		openAIError(c, http.StatusBadRequest, "invalid_request_error",
			"图像模型需要用户消息作为 prompt,请检查 messages 内容")
		return
	}
	explicitUpscale := requestedUpscaleFromOptions(req.Upscale, req.Resolution, req.ImageSize, req.Scale, req.Quality)
	imgReq := normalizeChatImageRequest(prompt, req)
	logImageRequestOptions("chat image generation options", imgReq, explicitUpscale)

	refID := uuid.NewString()

	// 倍率 + RPM
	ratio := 1.0
	rpmCap := ak.RPM
	if h.Groups != nil {
		if g, err := h.Groups.OfUser(c.Request.Context(), ak.UserID); err == nil && g != nil {
			ratio = g.Ratio
			if rpmCap == 0 {
				rpmCap = g.RPMLimit
			}
		}
	}
	if h.Limiter != nil {
		if ok, _, err := h.Limiter.AllowRPM(c.Request.Context(), ak.ID, rpmCap); err == nil && !ok {
			rec.Status = usage.StatusFailed
			rec.ErrorCode = "rate_limit_rpm"
			openAIError(c, http.StatusTooManyRequests, "rate_limit_rpm",
				"触发每分钟请求数限制 (RPM),请稍后再试")
			return
		}
	}
	if h.Channels != nil {
		channelReq := imageRequestForChannel(imgReq, explicitUpscale)
		if handled := h.dispatchChatImageToChannel(c, ak, m, channelReq, rec, ratio, startAt); handled {
			return
		}
		if channelReq.freeFallback {
			imgReq.freeFallback = true
			imgReq.freeFallbackDetail = channelReq.freeFallbackDetail
		}
	}

	// 预扣
	cost := billing.ComputeImageCost(m, imgReq.N, ratio)
	if cost > 0 {
		if err := h.Billing.PreDeduct(c.Request.Context(), ak.UserID, ak.ID, cost, refID, "chat->image prepay"); err != nil {
			rec.Status = usage.StatusFailed
			if errors.Is(err, billing.ErrInsufficient) {
				rec.ErrorCode = "insufficient_balance"
				openAIError(c, http.StatusPaymentRequired, "insufficient_balance",
					"积分不足,请前往「账单与充值」充值后再试")
				return
			}
			rec.ErrorCode = "billing_error"
			openAIError(c, http.StatusInternalServerError, "billing_error", "计费异常:"+err.Error())
			return
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
		_ = h.Billing.Refund(context.Background(), ak.UserID, ak.ID, cost, refID, "chat->image refund")
	}

	taskID := image.GenerateTaskID()
	if h.DAO != nil {
		task := &image.Task{
			TaskID:          taskID,
			UserID:          ak.UserID,
			KeyID:           ak.ID,
			ModelID:         m.ID,
			Prompt:          imgReq.Prompt,
			N:               imgReq.N,
			Size:            imgReq.Size,
			Upscale:         imgReq.Upscale,
			Status:          image.StatusDispatched,
			EstimatedCredit: cost,
		}
		downstreamUserInfoForTask(c, ak, imgReq.User).applyToTask(task)
		_ = h.DAO.Create(c.Request.Context(), task)
	}

	runCtx, cancel := context.WithTimeout(c.Request.Context(), 7*time.Minute)
	defer cancel()

	runAttempts, perAttemptTimeout, pollMaxWait, dispatchTimeout := asyncImageRunTuning(2, false)
	runOptions := image.RunOptions{
		TaskID:            taskID,
		UserID:            ak.UserID,
		KeyID:             ak.ID,
		ModelID:           m.ID,
		UpstreamModel:     m.UpstreamModelSlug,
		Prompt:            maybeAppendClaritySuffix(imgReq.Prompt),
		N:                 imgReq.N,
		MaxAttempts:       runAttempts,
		DispatchTimeout:   dispatchTimeout,
		PerAttemptTimeout: perAttemptTimeout,
		PollMaxWait:       pollMaxWait,
	}
	applyFreeFallbackPlan(&runOptions, imgReq.freeFallback)
	res := h.Runner.Run(runCtx, runOptions)
	rec.AccountID = res.AccountID

	if res.Status != image.StatusSuccess {
		refund(ifEmpty(res.ErrorCode, "upstream_error"))
		httpStatus := http.StatusBadGateway
		if res.ErrorCode == image.ErrNoAccount || res.ErrorCode == image.ErrRateLimited {
			httpStatus = http.StatusServiceUnavailable
		}
		openAIError(c, httpStatus, ifEmpty(res.ErrorCode, "upstream_error"),
			localizeImageErr(res.ErrorCode, res.ErrorMessage))
		return
	}

	if cost > 0 {
		_ = h.Billing.Settle(context.Background(), ak.UserID, ak.ID, cost, cost, refID, "chat->image settle")
	}
	_ = h.Keys.DAO().TouchUsage(context.Background(), ak.ID, c.ClientIP(), cost)
	if h.DAO != nil {
		_ = h.DAO.UpdateCost(c.Request.Context(), taskID, cost)
	}

	rec.Status = usage.StatusSuccess
	rec.CreditCost = cost
	rec.DurationMs = int(time.Since(startAt).Milliseconds())
	rec.ImageCount = imageCountFromSignedURLs(res.SignedURLs, 1)

	// 以 chat 响应返回(content 里内嵌 markdown 图片)。
	var sb strings.Builder
	for i := range res.SignedURLs {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(fmt.Sprintf("![generated](%s)", imageProxyURLForRequest(c, taskID, i)))
	}
	resp := ChatCompletionResponse{
		ID:      "chatcmpl-" + uuid.NewString(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   m.Slug,
		Choices: []ChatCompletionChoice{{
			Index: 0,
			Message: chatMsg{
				Role:    "assistant",
				Content: sb.String(),
			},
			FinishReason: "stop",
		}},
		Usage: ChatCompletionUsage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}
	c.JSON(http.StatusOK, resp)
}

// extractLastUserPrompt 从 messages 中拿最后一条 user 消息的 content。
func extractLastUserPrompt(msgs []chatMsg) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

func normalizeChatImageRequest(prompt string, req *ChatCompletionsRequest) *ImageGenRequest {
	n := req.N
	if n <= 0 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	size := req.Size
	if size == "" {
		size = "1024x1024"
	}
	return &ImageGenRequest{
		Model:             req.Model,
		Prompt:            prompt,
		N:                 n,
		Size:              size,
		Quality:           req.Quality,
		Style:             req.Style,
		ResponseFormat:    req.ResponseFormat,
		OutputFormat:      req.OutputFormat,
		OutputCompression: req.OutputCompression,
		Background:        req.Background,
		Moderation:        req.Moderation,
		Resolution:        req.Resolution,
		ImageSize:         req.ImageSize,
		Scale:             req.Scale,
		User:              req.User,
		Upscale:           normalizeImageUpscale(size, req.Upscale),
	}
}

func (r ImageGenRequest) referenceInputs() []string {
	var out []string
	seen := map[string]bool{}
	add := func(values stringList) {
		for _, value := range values {
			v := strings.TrimSpace(value)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	add(r.ReferenceImages)
	add(r.Images)
	add(r.Image)
	add(r.ImageURL)
	add(r.ImageURLs)
	add(r.InputImage)
	add(r.InputImages)
	return out
}

func requestedUpscaleFromOptions(values ...string) string {
	for _, value := range values {
		if scale := image.ValidateUpscale(value); scale != image.UpscaleNone {
			return scale
		}
		normalized := strings.ToLower(strings.TrimSpace(value))
		normalized = strings.ReplaceAll(normalized, " ", "")
		normalized = strings.ReplaceAll(normalized, "_", "")
		normalized = strings.ReplaceAll(normalized, "-", "")
		if strings.Contains(normalized, "4k") || strings.Contains(normalized, "uhd") || strings.Contains(normalized, "2160p") {
			return image.Upscale4K
		}
		if strings.Contains(normalized, "2k") || strings.Contains(normalized, "1440p") {
			return image.Upscale2K
		}
	}
	return image.UpscaleNone
}

func imageRequestForChannel(req *ImageGenRequest, explicitUpscale string) *ImageGenRequest {
	if req == nil {
		return nil
	}
	out := *req
	out.AspectRatio = req.Size
	if size := nativeImageChannelSize(req.Size, imageChannelLongSide(req, explicitUpscale)); size != "" {
		out.Size = size
	}
	if imageResolutionLongSide(out.Quality) > 0 {
		out.Quality = ""
	}
	return &out
}

func imageChannelLongSide(req *ImageGenRequest, explicitUpscale string) int {
	switch explicitUpscale {
	case image.Upscale4K:
		return 3840
	case image.Upscale2K:
		return 2048
	}
	if req == nil {
		return 0
	}
	return imageResolutionLongSide(req.Resolution, req.ImageSize, req.Scale, req.Upscale, req.Quality)
}

func imageResolutionLongSide(values ...string) int {
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		normalized = strings.ReplaceAll(normalized, " ", "")
		normalized = strings.ReplaceAll(normalized, "_", "")
		normalized = strings.ReplaceAll(normalized, "-", "")
		if normalized == "" {
			continue
		}
		if strings.Contains(normalized, "4k") || strings.Contains(normalized, "uhd") || strings.Contains(normalized, "2160p") {
			return 3840
		}
		if strings.Contains(normalized, "2k") || strings.Contains(normalized, "1440p") {
			return 2048
		}
		if strings.Contains(normalized, "1k") || strings.Contains(normalized, "1024p") || normalized == "1024" {
			return 1024
		}
	}
	return 0
}

func nativeImageChannelSize(size string, targetLongSide int) string {
	if _, _, ok := parseImageSize(size); ok {
		return ""
	}
	widthRatio, heightRatio, ok := parseImageAspectRatio(size)
	if !ok {
		if strings.EqualFold(strings.TrimSpace(size), "auto") && targetLongSide > 0 {
			widthRatio, heightRatio, ok = 1, 1, true
		} else {
			return ""
		}
	}
	if targetLongSide <= 0 {
		targetLongSide = 1024
	}
	const minPixelBudget = 1024 * 1024
	pixelBudget := targetLongSide * targetLongSide
	if targetLongSide >= 3840 {
		pixelBudget = 3840 * 2160
	}
	requiredPixelBudget := 0
	requiredLongSide := 0
	if targetLongSide <= 1024 {
		requiredPixelBudget = minPixelBudget
		if widthRatio != heightRatio {
			requiredLongSide = 1536
		}
	}
	maxRatioSide := widthRatio
	if heightRatio > maxRatioSide {
		maxRatioSide = heightRatio
	}
	maxScale := targetLongSide / maxRatioSide
	if maxScale <= 0 {
		return ""
	}
	for scale := maxScale; scale > 0; scale-- {
		width := widthRatio * scale
		height := heightRatio * scale
		if width%16 != 0 || height%16 != 0 {
			continue
		}
		area := width * height
		if area > pixelBudget {
			continue
		}
		if requiredPixelBudget > 0 && (area < requiredPixelBudget || maxInt(width, height) < requiredLongSide) {
			break
		}
		return fmt.Sprintf("%dx%d", width, height)
	}
	if requiredPixelBudget > 0 {
		upperScale := 3840 / maxRatioSide
		for scale := maxScale + 1; scale <= upperScale; scale++ {
			width := widthRatio * scale
			height := heightRatio * scale
			if width%16 != 0 || height%16 != 0 {
				continue
			}
			if width*height >= requiredPixelBudget && maxInt(width, height) >= requiredLongSide {
				return fmt.Sprintf("%dx%d", width, height)
			}
		}
	}
	return ""
}

func parseImageAspectRatio(size string) (int, int, bool) {
	s := strings.ToLower(strings.TrimSpace(size))
	if s == "" {
		return 0, 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	width, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	if div := gcd(width, height); div > 1 {
		width /= div
		height /= div
	}
	return width, height, true
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func logImageRequestOptions(msg string, req *ImageGenRequest, explicitUpscale string) {
	if req == nil {
		return
	}
	logger.L().Info(msg,
		zap.String("model", req.Model),
		zap.Int("n", req.N),
		zap.Int("reference_count", len(req.referenceInputs())),
		zap.String("size", req.Size),
		zap.String("quality", req.Quality),
		zap.String("output_format", req.OutputFormat),
		zap.String("background", req.Background),
		zap.String("moderation", req.Moderation),
		zap.String("resolution", req.Resolution),
		zap.String("image_size", req.ImageSize),
		zap.String("scale", req.Scale),
		zap.String("upscale", req.Upscale),
		zap.String("explicit_upscale", explicitUpscale),
	)
}

func normalizeImageUpscale(size, requested string) string {
	if v := image.ValidateUpscale(requested); v != image.UpscaleNone {
		return v
	}
	width, height, ok := parseImageSize(size)
	if !ok {
		return image.UpscaleNone
	}
	longSide := width
	if height > longSide {
		longSide = height
	}
	if longSide >= 3840 {
		return image.Upscale4K
	}
	if longSide >= 2048 {
		return image.Upscale2K
	}
	return image.UpscaleNone
}

func parseImageSize(size string) (int, int, bool) {
	s := strings.ToLower(strings.TrimSpace(size))
	if s == "" || s == "auto" {
		return 0, 0, false
	}
	parts := strings.Split(s, "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	width, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// --- helpers ---

func ifEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func asyncImageSubmitStatusCode() int { return http.StatusOK }

type apimartImageSubmitResponse struct {
	Code int                       `json:"code"`
	Data []apimartImageSubmitEntry `json:"data"`
}

type apimartImageSubmitEntry struct {
	Status string `json:"status"`
	TaskID string `json:"task_id"`
}

func writeAsyncImageSubmit(c *gin.Context, taskID string) {
	if oaierr.WantsAPIMart(c) {
		c.JSON(asyncImageSubmitStatusCode(), apimartImageSubmitResponse{
			Code: http.StatusOK,
			Data: []apimartImageSubmitEntry{{
				Status: "submitted",
				TaskID: taskID,
			}},
		})
		return
	}
	c.JSON(asyncImageSubmitStatusCode(), ImageGenResponse{
		Created: time.Now().Unix(),
		TaskID:  taskID,
		Data:    []ImageGenData{},
	})
}

func buildImageTaskPayload(t *image.Task, contexts ...*gin.Context) gin.H {
	out := gin.H{
		"task_id":         t.TaskID,
		"status":          t.Status,
		"conversation_id": t.ConversationID,
		"created":         t.CreatedAt.Unix(),
		"finished_at":     nullableUnix(t.FinishedAt),
		"error":           t.Error,
		"credit_cost":     t.CreditCost,
		"data":            imageTaskData(t, contexts...),
	}
	attachImageTaskErrorFields(out, t.Error, t.Status == image.StatusFailed)
	return out
}

func buildImageTaskCompatPayload(t *image.Task, contexts ...*gin.Context) gin.H {
	status, progress := imageTaskCompatStatus(t.Status)
	out := gin.H{
		"id":         t.TaskID,
		"task_id":    t.TaskID,
		"object":     "image.task",
		"status":     status,
		"progress":   progress,
		"created_at": t.CreatedAt.Unix(),
	}
	if t.FinishedAt != nil && !t.FinishedAt.IsZero() {
		out["completed_at"] = t.FinishedAt.Unix()
	}

	if t.Status == image.StatusSuccess {
		out["result"] = gin.H{
			"created": t.CreatedAt.Unix(),
			"data":    imageTaskData(t, contexts...),
		}
		return out
	}

	if t.Status == image.StatusFailed {
		code, detail, message := image.TaskErrorFields(t.Error)
		errorBody := gin.H{
			"code":    code,
			"message": message,
		}
		if detail != "" {
			errorBody["detail"] = detail
		}
		out["error"] = errorBody
		attachImageTaskErrorFields(out, t.Error, true)
	}
	return out
}

func attachImageTaskErrorFields(out gin.H, stored string, include bool) {
	if !include {
		return
	}
	code, detail, message := image.TaskErrorFields(stored)
	out["error_code"] = code
	out["error_message"] = message
	out["error_msg"] = message
	out["message"] = message
	out["failure_reason"] = message
	out["failed_reason"] = message
	out["fail_reason"] = message
	if detail != "" {
		out["error_detail"] = detail
	}
}

func imageTaskData(t *image.Task, contexts ...*gin.Context) []ImageGenData {
	urls := image.BuildTaskImageURLs(t, image.ImageProxyTTL)
	data := make([]ImageGenData, 0, len(urls))
	fileIDs := t.DecodeFileIDs()
	var c *gin.Context
	if len(contexts) > 0 {
		c = contexts[0]
	}
	for i, url := range urls {
		d := ImageGenData{URL: absoluteImageURLForRequest(c, url)}
		if i < len(fileIDs) {
			d.FileID = image.PublicFileID(fileIDs[i])
		}
		data = append(data, d)
	}
	return data
}

func imageProxyURLForRequest(c *gin.Context, taskID string, idx int) string {
	return absoluteImageURLForRequest(c, image.BuildImageProxyURL(taskID, idx, image.ImageProxyTTL))
}

func absoluteImageURLForRequest(c *gin.Context, rawURL string) string {
	if !strings.HasPrefix(rawURL, "/p/img/") {
		return rawURL
	}
	origin := requestOrigin(c)
	if origin == "" {
		return rawURL
	}
	return origin + rawURL
}

func requestOrigin(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	host := firstForwardedValue(c.GetHeader("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(c.Request.Host)
	}
	if !safeHTTPHost(host) {
		return ""
	}
	proto := strings.ToLower(firstForwardedValue(c.GetHeader("X-Forwarded-Proto")))
	if proto != "http" && proto != "https" {
		if c.Request.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	return proto + "://" + host
}

func firstForwardedValue(value string) string {
	if idx := strings.Index(value, ","); idx >= 0 {
		value = value[:idx]
	}
	return strings.TrimSpace(value)
}

func safeHTTPHost(host string) bool {
	if host == "" || strings.ContainsAny(host, "/\\@ \t\r\n") {
		return false
	}
	for _, r := range host {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func imageTaskCompatStatus(status string) (string, int) {
	switch status {
	case image.StatusSuccess:
		return "succeeded", 100
	case image.StatusFailed:
		return "failed", 100
	case image.StatusRunning:
		return "in_progress", 50
	case image.StatusQueued, image.StatusDispatched:
		return "queued", 0
	default:
		return "pending", 0
	}
}

func shouldWaitForImageResult(c *gin.Context, req ImageGenRequest) bool {
	if c != nil && c.Request != nil {
		if isTruthy(c.Query("async")) {
			return false
		}
		if isFalsey(c.Query("wait_for_result")) {
			return false
		}
		for _, part := range strings.Split(c.GetHeader("Prefer"), ",") {
			if strings.EqualFold(strings.TrimSpace(part), "respond-async") {
				return false
			}
		}
		if oaierr.WantsAPIMart(c) {
			return false
		}
	}
	if req.WaitForResult != nil {
		return *req.WaitForResult
	}
	return true
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func isFalsey(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

// localizeImageErr 把 runner 返回的英文错误码 + 原始 err.Error() 压成一段中文提示,
// 方便前端 / SDK 用户直接看懂。原始英文 message 作为后缀保留以便排障。
func localizeImageErr(code, raw string) string {
	return image.LocalizeTaskError(code, raw)
}

func nullableUnix(t *time.Time) int64 {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.Unix()
}

// 含这些关键字时,追加中英双约束让上游出字更清楚(迁移自 gen_image.py)。
var textHintKeywords = []string{
	"文字", "对话", "台词", "旁白", "标语", "字幕", "标题", "文案",
	"招牌", "横幅", "海报文字", "弹幕", "气泡", "字体",
	"text:", "caption", "subtitle", "title:", "label", "banner", "poster text",
}

const claritySuffix = "\n\nclean readable Chinese text, prioritize text clarity over image details"

// ImageEdits 实现 POST /v1/images/edits,严格按 OpenAI 规范接 multipart/form-data。
//
// 表单字段(与 OpenAI 官方一致):
//
//	image            (file)      单张主图,必填
//	image[]          (file)      多张,可重复(2025 起官方支持)
//	mask             (file)      可选,透明区域为编辑区;当前协议下直接一并上传(上游暂不区分)
//	prompt           (string)    必填
//	model            (string)    模型 slug,默认 gpt-image-2
//	n                (int)       默认 1
//	size             (string)    默认 1024x1024
//	response_format  (string)    url | b64_json,当前仅 url
//	user             (string)
//
// 实际走的上游协议和 /v1/images/generations + reference_images 完全相同。
// 行为等价于"把 multipart 文件读成字节 + prompt,交给 ImageGenerations 的主流程"。
func (h *ImagesHandler) ImageEdits(c *gin.Context) {
	startAt := time.Now()
	ak, ok := apikey.FromCtx(c)
	if !ok {
		openAIError(c, http.StatusUnauthorized, "missing_api_key", "缺少 API Key")
		return
	}

	// multipart 上限:单文件 20MB * 最多 4 张 + 冗余。
	if err := c.Request.ParseMultipartForm(int64(maxReferenceImageBytes) * int64(maxReferenceImages+1)); err != nil {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "解析 multipart 失败:"+err.Error())
		return
	}

	prompt := strings.TrimSpace(c.Request.FormValue("prompt"))
	if prompt == "" {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "prompt 不能为空")
		return
	}
	model := c.Request.FormValue("model")
	if model == "" {
		model = "gpt-image-2"
	}
	n := 1
	if s := c.Request.FormValue("n"); s != "" {
		if v, err := parseIntClamp(s, 1, 4); err == nil {
			n = v
		}
	}
	size := c.Request.FormValue("size")
	if size == "" {
		size = "1024x1024"
	}
	quality := c.Request.FormValue("quality")
	resolution := c.Request.FormValue("resolution")
	imageSize := c.Request.FormValue("image_size")
	scale := c.Request.FormValue("scale")
	explicitUpscale := requestedUpscaleFromOptions(c.Request.FormValue("upscale"), resolution, imageSize, scale, quality)
	upscale := normalizeImageUpscale(size, explicitUpscale)

	// 主图 + 可能的多张
	files, err := collectEditFiles(c.Request.MultipartForm)
	if err != nil {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if len(files) == 0 {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "至少需要上传一张 image 作为参考图")
		return
	}
	if len(files) > maxReferenceImages {
		openAIError(c, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("最多支持 %d 张参考图", maxReferenceImages))
		return
	}
	refs := make([]image.ReferenceImage, 0, len(files))
	for _, fh := range files {
		data, err := readMultipart(fh)
		if err != nil {
			openAIError(c, http.StatusBadRequest, "invalid_reference_image",
				fmt.Sprintf("读取 %q 失败:%s", fh.Filename, err.Error()))
			return
		}
		if len(data) == 0 {
			openAIError(c, http.StatusBadRequest, "invalid_reference_image",
				fmt.Sprintf("参考图 %q 为空", fh.Filename))
			return
		}
		if len(data) > maxReferenceImageBytes {
			openAIError(c, http.StatusBadRequest, "invalid_reference_image",
				fmt.Sprintf("参考图 %q 超过 %dMB 上限", fh.Filename, maxReferenceImageBytes/1024/1024))
			return
		}
		refs = append(refs, image.ReferenceImage{Data: data, FileName: filepath.Base(fh.Filename)})
	}

	// usage 记录
	refID := uuid.NewString()
	rec := &usage.Log{
		UserID:    ak.UserID,
		KeyID:     ak.ID,
		RequestID: refID,
		Type:      usage.TypeImage,
		IP:        c.ClientIP(),
		UA:        c.Request.UserAgent(),
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
	fail := func(code string) { rec.Status = usage.StatusFailed; rec.ErrorCode = code }

	if !ak.ModelAllowed(model) {
		fail("model_not_allowed")
		openAIError(c, http.StatusForbidden, "model_not_allowed",
			fmt.Sprintf("当前 API Key 无权调用模型 %q", model))
		return
	}
	m, err := h.Models.BySlug(c.Request.Context(), model)
	if err != nil || m == nil || !m.Enabled {
		fail("model_not_found")
		openAIError(c, http.StatusBadRequest, "model_not_found",
			fmt.Sprintf("模型 %q 不存在或已下架", model))
		return
	}
	if m.Type != modelpkg.TypeImage {
		fail("model_type_mismatch")
		openAIError(c, http.StatusBadRequest, "model_type_mismatch",
			fmt.Sprintf("模型 %q 不是图像模型,不能用于 /v1/images/edits", model))
		return
	}
	rec.ModelID = m.ID

	ratio := 1.0
	rpmCap := ak.RPM
	if h.Groups != nil {
		if g, err := h.Groups.OfUser(c.Request.Context(), ak.UserID); err == nil && g != nil {
			ratio = g.Ratio
			if rpmCap == 0 {
				rpmCap = g.RPMLimit
			}
		}
	}
	if h.Limiter != nil {
		if ok, _, err := h.Limiter.AllowRPM(c.Request.Context(), ak.ID, rpmCap); err == nil && !ok {
			fail("rate_limit_rpm")
			openAIError(c, http.StatusTooManyRequests, "rate_limit_rpm",
				"触发每分钟请求数限制 (RPM),请稍后再试")
			return
		}
	}

	freeFallback := false
	if h.Channels != nil {
		editReq := &ImageGenRequest{
			Model:          model,
			Prompt:         prompt,
			N:              n,
			Size:           size,
			Quality:        quality,
			ResponseFormat: c.Request.FormValue("response_format"),
			OutputFormat:   c.Request.FormValue("output_format"),
			Background:     c.Request.FormValue("background"),
			Moderation:     c.Request.FormValue("moderation"),
			Resolution:     resolution,
			ImageSize:      imageSize,
			Scale:          scale,
			Upscale:        explicitUpscale,
			User:           c.Request.FormValue("user"),
		}
		channelReq := imageRequestForChannel(editReq, explicitUpscale)
		if handled := h.dispatchImageToChannel(c, ak, m, channelReq, rec, ratio, refs); handled {
			return
		}
		freeFallback = channelReq.freeFallback
	}

	cost := billing.ComputeImageCost(m, n, ratio)
	if cost > 0 {
		if err := h.Billing.PreDeduct(c.Request.Context(), ak.UserID, ak.ID, cost, refID, "image-edit prepay"); err != nil {
			if errors.Is(err, billing.ErrInsufficient) {
				fail("insufficient_balance")
				openAIError(c, http.StatusPaymentRequired, "insufficient_balance",
					"积分不足,请前往「账单与充值」充值后再试")
				return
			}
			fail("billing_error")
			openAIError(c, http.StatusInternalServerError, "billing_error", "计费异常:"+err.Error())
			return
		}
	}
	refunded := false
	refund := func(code string) {
		fail(code)
		if refunded || cost == 0 {
			return
		}
		refunded = true
		_ = h.Billing.Refund(context.Background(), ak.UserID, ak.ID, cost, refID, "image-edit refund")
	}

	taskID := image.GenerateTaskID()
	if h.DAO != nil {
		task := &image.Task{
			TaskID:          taskID,
			UserID:          ak.UserID,
			KeyID:           ak.ID,
			ModelID:         m.ID,
			Prompt:          prompt,
			N:               n,
			Size:            size,
			Upscale:         upscale,
			Status:          image.StatusDispatched,
			EstimatedCredit: cost,
		}
		downstreamUserInfoForTask(c, ak, c.Request.FormValue("user")).applyToTask(task)
		_ = h.DAO.Create(c.Request.Context(), task)
	}

	runCtx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Minute)
	defer cancel()

	runAttempts, perAttemptTimeout, pollMaxWait, dispatchTimeout := asyncImageRunTuning(2, true)
	runOptions := image.RunOptions{
		TaskID:            taskID,
		UserID:            ak.UserID,
		KeyID:             ak.ID,
		ModelID:           m.ID,
		UpstreamModel:     m.UpstreamModelSlug,
		Prompt:            maybeAppendClaritySuffix(prompt),
		N:                 n,
		MaxAttempts:       runAttempts,
		DispatchTimeout:   dispatchTimeout,
		PerAttemptTimeout: perAttemptTimeout,
		PollMaxWait:       pollMaxWait,
		References:        refs,
	}
	applyFreeFallbackPlan(&runOptions, freeFallback)
	res := h.Runner.Run(runCtx, runOptions)
	rec.AccountID = res.AccountID

	if res.Status != image.StatusSuccess {
		refund(ifEmpty(res.ErrorCode, "upstream_error"))
		httpStatus := http.StatusBadGateway
		if res.ErrorCode == image.ErrNoAccount || res.ErrorCode == image.ErrRateLimited {
			httpStatus = http.StatusServiceUnavailable
		}
		openAIError(c, httpStatus, ifEmpty(res.ErrorCode, "upstream_error"),
			localizeImageErr(res.ErrorCode, res.ErrorMessage))
		return
	}

	if cost > 0 {
		if err := h.Billing.Settle(context.Background(), ak.UserID, ak.ID, cost, cost, refID, "image-edit settle"); err != nil {
			logger.L().Error("billing settle image-edit", zap.Error(err), zap.String("ref", refID))
		}
	}
	_ = h.Keys.DAO().TouchUsage(context.Background(), ak.ID, c.ClientIP(), cost)

	rec.Status = usage.StatusSuccess
	rec.CreditCost = cost
	rec.ImageCount = imageCountFromSignedURLs(res.SignedURLs, n)
	if h.DAO != nil {
		_ = h.DAO.UpdateCost(c.Request.Context(), taskID, cost)
	}

	out := ImageGenResponse{
		Created: time.Now().Unix(),
		TaskID:  taskID,
		Data:    make([]ImageGenData, 0, len(res.SignedURLs)),
	}
	for i := range res.SignedURLs {
		d := ImageGenData{URL: imageProxyURLForRequest(c, taskID, i)}
		if i < len(res.FileIDs) {
			d.FileID = image.PublicFileID(res.FileIDs[i])
		}
		out.Data = append(out.Data, d)
	}
	c.JSON(http.StatusOK, out)
}

// collectEditFiles 把 multipart 里"可能作为参考图"的字段一次性收拢。
// 兼容 OpenAI 的几种写法:
//   - image      : 单文件
//   - image[]    : 多文件
//   - mask       : 可选,按参考图一并喂给上游(上游暂不区分 mask)
func collectEditFiles(form *multipart.Form) ([]*multipart.FileHeader, error) {
	if form == nil {
		return nil, errors.New("empty multipart form")
	}
	var out []*multipart.FileHeader
	seen := map[string]bool{}
	add := func(fhs []*multipart.FileHeader) {
		for _, fh := range fhs {
			if fh == nil {
				continue
			}
			key := fh.Filename + "|" + fmt.Sprint(fh.Size)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, fh)
		}
	}
	for _, key := range []string{"image", "image[]", "images", "images[]", "mask"} {
		if fhs := form.File[key]; len(fhs) > 0 {
			add(fhs)
		}
	}
	// 也兼容 image_1 / image_2 / ... 的写法
	for k, fhs := range form.File {
		if strings.HasPrefix(k, "image_") {
			add(fhs)
		}
	}
	return out, nil
}

func readMultipart(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// decodeReferenceInputs 把 JSON 里 reference_images(url/data-url/base64 混合)下载/解码成字节。
// 超出条数上限直接报错;单张尺寸上限 maxReferenceImageBytes。
func decodeReferenceInputs(ctx context.Context, inputs []string) ([]image.ReferenceImage, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if len(inputs) > maxReferenceImages {
		return nil, fmt.Errorf("最多支持 %d 张参考图", maxReferenceImages)
	}
	out := make([]image.ReferenceImage, 0, len(inputs))
	for i, s := range inputs {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("第 %d 张参考图为空", i+1)
		}
		data, name, err := fetchReferenceBytes(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("第 %d 张参考图:%w", i+1, err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("第 %d 张参考图解码后为空", i+1)
		}
		if len(data) > maxReferenceImageBytes {
			return nil, fmt.Errorf("第 %d 张参考图超过 %dMB 上限", i+1, maxReferenceImageBytes/1024/1024)
		}
		out = append(out, image.ReferenceImage{Data: data, FileName: name})
	}
	return out, nil
}

// fetchReferenceBytes 支持 http(s) / data URL / 裸 base64 三种输入。
func fetchReferenceBytes(ctx context.Context, s string) ([]byte, string, error) {
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(low, "data:"):
		// data:<mime>[;base64],<payload>
		comma := strings.IndexByte(s, ',')
		if comma < 0 {
			return nil, "", errors.New("无效 data URL")
		}
		meta := s[5:comma]
		payload := s[comma+1:]
		if strings.Contains(strings.ToLower(meta), ";base64") {
			b, err := base64.StdEncoding.DecodeString(payload)
			if err != nil {
				// 兼容 URL-safe
				if b2, err2 := base64.URLEncoding.DecodeString(payload); err2 == nil {
					b = b2
				} else {
					return nil, "", fmt.Errorf("base64 解码失败:%w", err)
				}
			}
			return b, "", nil
		}
		return []byte(payload), "", nil
	case strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "https://"):
		return fetchReferenceHTTPBytes(ctx, s)
	default:
		// 当成裸 base64 处理
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			if b2, err2 := base64.URLEncoding.DecodeString(s); err2 == nil {
				return b2, "", nil
			}
			return nil, "", fmt.Errorf("既非 URL 也非可解析的 base64:%w", err)
		}
		return b, "", nil
	}
}

type referenceFetchHTTPStatusError struct {
	StatusCode int
}

func (e referenceFetchHTTPStatusError) Error() string {
	return fmt.Sprintf("下载失败 HTTP %d", e.StatusCode)
}

func fetchReferenceHTTPBytes(ctx context.Context, rawURL string) ([]byte, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	source := referenceFetchSourceLabel(rawURL)
	var lastErr error
	for attempt := 1; attempt <= referenceFetchMaxAttempt; attempt++ {
		data, name, err := fetchReferenceHTTPBytesOnce(ctx, rawURL)
		if err == nil {
			return data, name, nil
		}
		lastErr = err
		if attempt >= referenceFetchMaxAttempt || !isRetryableReferenceFetchErr(err) {
			return nil, "", err
		}
		logger.L().Warn("reference image fetch transient fail, retry",
			zap.String("source", source),
			zap.Int("attempt", attempt),
			zap.Error(err))
		if err := sleepWithContext(ctx, time.Duration(attempt)*300*time.Millisecond); err != nil {
			return nil, "", lastErr
		}
	}
	return nil, "", lastErr
}

func fetchReferenceHTTPBytesOnce(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	hc := &http.Client{Timeout: referenceFetchTimeout}
	res, err := hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, "", referenceFetchHTTPStatusError{StatusCode: res.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, int64(maxReferenceImageBytes)+1))
	if err != nil {
		return nil, "", err
	}
	name := filepath.Base(req.URL.Path)
	return body, name, nil
}

func isRetryableReferenceFetchErr(err error) bool {
	if err == nil {
		return false
	}
	var statusErr referenceFetchHTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode == http.StatusRequestTimeout ||
			statusErr.StatusCode == http.StatusTooManyRequests ||
			statusErr.StatusCode >= http.StatusInternalServerError
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe")
}

func referenceFetchSourceLabel(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return u.Host + path
}

func parseIntClamp(s string, min, max int) (int, error) {
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, err
	}
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return v, nil
}

func imageCountFromSignedURLs(signedURLs []string, requested int) int {
	if len(signedURLs) > 0 {
		return len(signedURLs)
	}
	if requested > 0 {
		return requested
	}
	return 1
}

func maybeAppendClaritySuffix(prompt string) string {
	lower := strings.ToLower(prompt)
	need := false
	for _, kw := range textHintKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			need = true
			break
		}
	}
	if !need {
		// 检测中文/英文引号内容 ≥ 2 个字
		for _, pair := range [][2]string{
			{"\"", "\""}, {"'", "'"},
			{"“", "”"}, {"‘", "’"},
			{"「", "」"}, {"『", "』"},
		} {
			if idx := strings.Index(prompt, pair[0]); idx >= 0 {
				rest := prompt[idx+len(pair[0]):]
				if end := strings.Index(rest, pair[1]); end >= 2 {
					need = true
					break
				}
			}
		}
	}
	if need && !strings.Contains(prompt, strings.TrimSpace(claritySuffix)) {
		return prompt + claritySuffix
	}
	return prompt
}
