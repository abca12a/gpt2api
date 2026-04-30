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
	policy := h.imageFallbackPolicy()
	routes, skippedSteps := prepareImageRoutes(routes, policy)
	trace := ensureRequestTrace(req)
	if trace != nil {
		for _, step := range skippedSteps {
			trace.AddStep(step)
		}
	}
	if trace != nil && trace.Original.Provider == "" && len(routes) > 0 {
		trace.Original = imagepkg.TaskTraceEndpoint{
			Provider:    imageProviderForRoute(routes[0]),
			ChannelID:   routes[0].Channel.ID,
			ChannelName: routes[0].Channel.Name,
		}
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
	if _, err := h.ensureTaskRecord(c, ak, m, req, cost, trace); err != nil {
		refund("billing_error")
		openAIError(c, http.StatusInternalServerError, "internal_error", "创建任务失败:"+err.Error())
		return true
	}
	h.persistRequestTrace(c.Request.Context(), req, trace)
	if h.DAO != nil && req.taskID != "" {
		_ = h.DAO.MarkRunning(c.Request.Context(), req.taskID, 0)
	}

	ir := imageAdapterRequest(m, req, refs)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 7*time.Minute)
	defer cancel()

	var lastErr error
	var lastFailure imageChannelFailure
	var lastFailedStep imagepkg.TaskTraceStep
	var result *adapter.ImageResult
	var selected *channel.Route
	for _, rt := range routes {
		step := imageTraceStepForRoute(rt)
		observer := &imageChannelGenerateObserver{}
		routeStart := time.Now()
		r, err := imageChannelGenerateWithRetry(adapter.WithImageGenerateObserver(ctx, observer), rt, ir, "", 0, nil)
		recordImageChannelRouteTiming(trace, req.taskID, time.Since(routeStart), observer,
			zap.Uint64("channel_id", rt.Channel.ID),
			zap.String("channel_name", rt.Channel.Name),
		)
		if err != nil {
			lastErr = err
			lastFailure = imageChannelFailureFromErr(err)
			step.Status = imagepkg.StatusFailed
			step.ReasonCode = lastFailure.Code
			step.ReasonDetail = ifEmpty(lastFailure.Detail, err.Error())
			lastFailedStep = step
			trace.AddStep(step)
			h.persistRequestTrace(c.Request.Context(), req, trace)
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
		step.Status = imagepkg.StatusSuccess
		trace.AddStep(step)
		h.persistRequestTrace(c.Request.Context(), req, trace)
		break
	}

	if result == nil {
		fallbackStep := lastFailedStep
		fallbackCode := lastFailure.Code
		fallbackDetail := lastFailure.Detail
		if fallbackStep.Provider == "" && len(skippedSteps) > 0 {
			fallbackStep = skippedSteps[len(skippedSteps)-1]
		}
		if fallbackCode == "" {
			fallbackCode = fallbackStep.ReasonCode
		}
		if fallbackDetail == "" {
			fallbackDetail = fallbackStep.ReasonDetail
		}
		if (len(routes) == 0 || shouldFallbackImageChannelToFree(lastErr)) && h.Runner != nil && req != nil && len(policy.RunnerPlans) > 0 {
			trace.MarkFallback(fallbackStep, fallbackCode, fallbackDetail)
			h.persistRequestTrace(c.Request.Context(), req, trace)
			logFields := []zap.Field{zap.String("model", m.Slug)}
			if lastErr != nil {
				logFields = append(logFields, zap.Error(lastErr))
			} else {
				logFields = append(logFields, zap.String("reason", fallbackDetail))
			}
			logger.L().Warn("channel image fallback to account runner", logFields...)
			refund(imagepkg.ErrUpstream)
			req.runnerFallbackPlans = cloneImageRunnerFallbackPlans(policy.RunnerPlans)
			req.freeFallback = isFreeRunnerPlan(firstImageRunnerFallbackPlan(req.runnerFallbackPlans))
			if lastErr != nil {
				req.freeFallbackDetail = lastErr.Error()
			} else {
				req.freeFallbackDetail = fallbackDetail
			}
			return false
		}
		failure := imageChannelFailureFromErr(lastErr)
		if h.DAO != nil && req.taskID != "" {
			_ = h.DAO.MarkFailedDetail(c.Request.Context(), req.taskID, failure.Code, failure.Detail)
		}
		imagepkg.LogTaskLifecycle(req.taskID, trace, imagepkg.StatusFailed, failure.Code,
			zap.String("mode", "channel"),
		)
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
	if h.DAO != nil && req.taskID != "" {
		_ = h.DAO.MarkSuccess(c.Request.Context(), req.taskID, "", nil, imageChannelResultURLs(result), finalCost)
		h.persistRequestTrace(c.Request.Context(), req, trace)
	}
	imagepkg.LogTaskLifecycle(req.taskID, trace, imagepkg.StatusSuccess, "",
		zap.String("mode", "channel"),
	)

	c.JSON(http.StatusOK, ImageGenResponse{
		Created: time.Now().Unix(),
		TaskID:  req.taskID,
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
	policy := h.imageFallbackPolicy()
	routes, skippedSteps := prepareImageRoutes(routes, policy)
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
	trace := ensureRequestTrace(req)
	if trace != nil {
		for _, step := range skippedSteps {
			trace.AddStep(step)
		}
	}
	if trace != nil && trace.Original.Provider == "" && len(routes) > 0 {
		trace.Original = imagepkg.TaskTraceEndpoint{
			Provider:    imageProviderForRoute(routes[0]),
			ChannelID:   routes[0].Channel.ID,
			ChannelName: routes[0].Channel.Name,
		}
	}
	taskID, err := h.ensureTaskRecord(c, ak, m, req, cost, trace)
	if err != nil {
		rec.Status = usage.StatusFailed
		rec.ErrorCode = "billing_error"
		if cost > 0 {
			_ = h.Billing.Refund(context.Background(), ak.UserID, ak.ID, cost, refID, "image channel async create refund")
		}
		openAIError(c, http.StatusInternalServerError, "internal_error", "创建任务失败:"+err.Error())
		return true, false
	}
	h.persistRequestTrace(c.Request.Context(), req, trace)

	h.runImageChannelTaskAsync(imageChannelAsyncJob{
		TaskID:        taskID,
		UserID:        ak.UserID,
		KeyID:         ak.ID,
		ModelID:       m.ID,
		Model:         m,
		Ratio:         ratio,
		Routes:        routes,
		Request:       imageAdapterRequest(m, req, refs),
		References:    refs,
		Cost:          cost,
		RefID:         refID,
		IP:            c.ClientIP(),
		UA:            c.Request.UserAgent(),
		ProviderTrace: trace,
		RunnerPlans:   cloneImageRunnerFallbackPlans(policy.RunnerPlans),
	})
	writeAsyncImageSubmit(c, taskID)
	return true, true
}

type imageChannelAsyncJob struct {
	TaskID        string
	UserID        uint64
	KeyID         uint64
	ModelID       uint64
	Model         *modelpkg.Model
	Ratio         float64
	Routes        []*channel.Route
	Request       *adapter.ImageRequest
	References    []imagepkg.ReferenceImage
	Cost          int64
	RefID         string
	IP            string
	UA            string
	ProviderTrace *imagepkg.TaskTrace
	RunnerPlans   []imageRunnerFallbackPlan
}

type imageChannelGenerateObserver struct {
	submit time.Duration
	poll   time.Duration
}

func (o *imageChannelGenerateObserver) RecordSubmitDuration(d time.Duration) {
	if o == nil || d <= 0 {
		return
	}
	o.submit += d
}

func (o *imageChannelGenerateObserver) RecordPollDuration(d time.Duration) {
	if o == nil || d <= 0 {
		return
	}
	o.poll += d
}

func recordImageChannelRouteTiming(trace *imagepkg.TaskTrace, taskID string, total time.Duration, observer *imageChannelGenerateObserver, fields ...zap.Field) {
	if trace == nil || total <= 0 {
		return
	}
	var submitDuration time.Duration
	var pollDuration time.Duration
	if observer != nil {
		submitDuration = observer.submit
		pollDuration = observer.poll
	}
	waitDuration := total - submitDuration - pollDuration
	if waitDuration < 0 {
		waitDuration = 0
	}
	if submitDuration > 0 {
		trace.AddSubmitDuration(submitDuration)
		imagepkg.LogTaskStage(taskID, imagepkg.TaskPhaseSubmit, submitDuration, fields...)
	}
	if waitDuration > 0 {
		trace.AddUpstreamWaitDuration(waitDuration)
		imagepkg.LogTaskStage(taskID, imagepkg.TaskPhaseUpstreamWait, waitDuration, fields...)
	}
	if pollDuration > 0 {
		trace.AddPollDuration(pollDuration)
		imagepkg.LogTaskStage(taskID, imagepkg.TaskPhaseTaskPoll, pollDuration, fields...)
	}
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
		trace := job.ProviderTrace
		hasReferences := len(job.References) > 0
		taskCtx, cancelTask := context.WithTimeout(context.Background(), imageChannelTaskTimeoutForRoutes(job.Routes, hasReferences))
		defer cancelTask()
		channelCtx, cancelChannel := context.WithTimeout(taskCtx, imageChannelRoutesTimeout(job.Routes, hasReferences))
		defer cancelChannel()

		var lastErr error
		var lastFailure imageChannelFailure
		var lastFailedStep imagepkg.TaskTraceStep
		var result *adapter.ImageResult
		var selected *channel.Route
		defaultPerAttemptTimeout := imageChannelAsyncPerAttemptTimeout(hasReferences)
		for _, rt := range job.Routes {
			step := imageTraceStepForRoute(rt)
			routeCtx := channelCtx
			cancelRoute := func() {}
			routeTimeout := imageChannelRouteTimeout(rt, hasReferences)
			if routeTimeout > 0 {
				routeCtx, cancelRoute = context.WithTimeout(channelCtx, routeTimeout)
			}
			observer := &imageChannelGenerateObserver{}
			routeStart := time.Now()
			perAttemptTimeout := routeTimeout
			if perAttemptTimeout <= 0 {
				perAttemptTimeout = defaultPerAttemptTimeout
			}
			r, err := imageChannelGenerateWithRetry(adapter.WithImageGenerateObserver(routeCtx, observer), rt, job.Request, job.TaskID, perAttemptTimeout, nil)
			cancelRoute()
			recordImageChannelRouteTiming(trace, job.TaskID, time.Since(routeStart), observer,
				zap.Uint64("channel_id", rt.Channel.ID),
				zap.String("channel_name", rt.Channel.Name),
			)
			if err != nil {
				lastErr = err
				lastFailure = imageChannelFailureFromErr(err)
				step.Status = imagepkg.StatusFailed
				step.ReasonCode = lastFailure.Code
				step.ReasonDetail = ifEmpty(lastFailure.Detail, err.Error())
				lastFailedStep = step
				if trace != nil {
					trace.AddStep(step)
					_ = h.DAO.UpdateProviderTrace(context.Background(), job.TaskID, trace)
				}
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
			step.Status = imagepkg.StatusSuccess
			if trace != nil {
				trace.AddStep(step)
				_ = h.DAO.UpdateProviderTrace(context.Background(), job.TaskID, trace)
			}
			break
		}

		if result == nil {
			fallbackStep := lastFailedStep
			fallbackCode := lastFailure.Code
			fallbackDetail := lastFailure.Detail
			if fallbackStep.Provider == "" && trace != nil && len(trace.Steps) > 0 {
				lastStep := trace.Steps[len(trace.Steps)-1]
				if lastStep.Status == imageFallbackStatusSkipped {
					fallbackStep = lastStep
				}
			}
			if fallbackCode == "" {
				fallbackCode = fallbackStep.ReasonCode
			}
			if fallbackDetail == "" {
				fallbackDetail = fallbackStep.ReasonDetail
			}
			if (len(job.Routes) == 0 || shouldFallbackImageChannelToFree(lastErr)) && h.Runner != nil && len(job.RunnerPlans) > 0 {
				if trace != nil {
					trace.MarkFallback(fallbackStep, fallbackCode, fallbackDetail)
					_ = h.DAO.UpdateProviderTrace(context.Background(), job.TaskID, trace)
				}
				logFields := []zap.Field{zap.String("task_id", job.TaskID)}
				if lastErr != nil {
					logFields = append(logFields, zap.Error(lastErr))
				} else {
					logFields = append(logFields, zap.String("reason", fallbackDetail))
				}
				logger.L().Warn("channel async image fallback to account runner", logFields...)
				fallbackOpt := imageChannelFallbackRunOptions(job)
				fallbackCtx, cancelFallback := withImageChannelFallbackContext(taskCtx, fallbackOpt.MaxAttempts, hasReferences)
				fallback, usedPlan := h.runImageWithFallbackPlans(fallbackCtx, fallbackOpt, job.RunnerPlans)
				cancelFallback()
				rec.AccountID = fallback.AccountID
				if trace != nil {
					trace.AddStep(imageTraceStepForRunnerPlan(fallback, usedPlan, fallback.Status, fallback.ErrorCode, fallback.ErrorMessage))
					_ = h.DAO.UpdateProviderTrace(context.Background(), job.TaskID, trace)
				}
				if fallback.Status == imagepkg.StatusSuccess {
					if job.Cost > 0 && h.Billing != nil {
						if err := h.Billing.Settle(context.Background(), job.UserID, job.KeyID, job.Cost, job.Cost, job.RefID, "image channel async runner fallback settle"); err != nil {
							logger.L().Error("billing settle async runner fallback image", zap.Error(err), zap.String("ref", job.RefID))
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
					imagepkg.LogTaskLifecycle(job.TaskID, trace, imagepkg.StatusSuccess, "",
						zap.String("mode", "channel_runner_fallback"),
					)
					return
				}
				rec.Status = usage.StatusFailed
				rec.ErrorCode = ifEmpty(fallback.ErrorCode, imagepkg.ErrUpstream)
				if job.Cost > 0 && h.Billing != nil {
					_ = h.Billing.Refund(context.Background(), job.UserID, job.KeyID, job.Cost, job.RefID, "image channel async runner fallback refund")
				}
				imagepkg.LogTaskLifecycle(job.TaskID, trace, imagepkg.StatusFailed, rec.ErrorCode,
					zap.String("mode", "channel_runner_fallback"),
				)
				return
			}
			failure := imageChannelFailureFromErr(lastErr)
			rec.Status = usage.StatusFailed
			rec.ErrorCode = failure.Code
			if h.DAO != nil {
				_ = h.DAO.MarkFailedDetail(context.Background(), job.TaskID, failure.Code, failure.Detail)
				if trace != nil {
					_ = h.DAO.UpdateProviderTrace(context.Background(), job.TaskID, trace)
				}
			}
			if job.Cost > 0 && h.Billing != nil {
				_ = h.Billing.Refund(context.Background(), job.UserID, job.KeyID, job.Cost, job.RefID, "image channel async refund")
			}
			imagepkg.LogTaskLifecycle(job.TaskID, trace, imagepkg.StatusFailed, failure.Code,
				zap.String("mode", "channel_async"),
			)
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
			if trace != nil {
				_ = h.DAO.UpdateProviderTrace(context.Background(), job.TaskID, trace)
			}
		}
		rec.Status = usage.StatusSuccess
		rec.CreditCost = finalCost
		rec.ImageCount = actualCount(result)
		imagepkg.LogTaskLifecycle(job.TaskID, trace, imagepkg.StatusSuccess, "",
			zap.String("mode", "channel_async"),
		)
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
	policy := h.imageFallbackPolicy()
	routes, skippedSteps := prepareImageRoutes(routes, policy)
	trace := ensureRequestTrace(req)
	if trace != nil {
		for _, step := range skippedSteps {
			trace.AddStep(step)
		}
	}
	if trace != nil && trace.Original.Provider == "" && len(routes) > 0 {
		trace.Original = imagepkg.TaskTraceEndpoint{
			Provider:    imageProviderForRoute(routes[0]),
			ChannelID:   routes[0].Channel.ID,
			ChannelName: routes[0].Channel.Name,
		}
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
	if _, err := h.ensureTaskRecord(c, ak, m, req, cost, trace); err != nil {
		refund("billing_error")
		openAIError(c, http.StatusInternalServerError, "internal_error", "创建任务失败:"+err.Error())
		return true
	}
	h.persistRequestTrace(c.Request.Context(), req, trace)
	if h.DAO != nil && req.taskID != "" {
		_ = h.DAO.MarkRunning(c.Request.Context(), req.taskID, 0)
	}

	ir := imageAdapterRequest(m, req, nil)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 7*time.Minute)
	defer cancel()

	var lastErr error
	var lastFailure imageChannelFailure
	var lastFailedStep imagepkg.TaskTraceStep
	var result *adapter.ImageResult
	var selected *channel.Route
	for _, rt := range routes {
		step := imageTraceStepForRoute(rt)
		observer := &imageChannelGenerateObserver{}
		routeStart := time.Now()
		r, err := imageChannelGenerateWithRetry(adapter.WithImageGenerateObserver(ctx, observer), rt, ir, "", 0, nil)
		recordImageChannelRouteTiming(trace, req.taskID, time.Since(routeStart), observer,
			zap.Uint64("channel_id", rt.Channel.ID),
			zap.String("channel_name", rt.Channel.Name),
		)
		if err != nil {
			lastErr = err
			lastFailure = imageChannelFailureFromErr(err)
			step.Status = imagepkg.StatusFailed
			step.ReasonCode = lastFailure.Code
			step.ReasonDetail = ifEmpty(lastFailure.Detail, err.Error())
			lastFailedStep = step
			trace.AddStep(step)
			h.persistRequestTrace(c.Request.Context(), req, trace)
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
		step.Status = imagepkg.StatusSuccess
		trace.AddStep(step)
		h.persistRequestTrace(c.Request.Context(), req, trace)
		break
	}

	if result == nil {
		fallbackStep := lastFailedStep
		fallbackCode := lastFailure.Code
		fallbackDetail := lastFailure.Detail
		if fallbackStep.Provider == "" && len(skippedSteps) > 0 {
			fallbackStep = skippedSteps[len(skippedSteps)-1]
		}
		if fallbackCode == "" {
			fallbackCode = fallbackStep.ReasonCode
		}
		if fallbackDetail == "" {
			fallbackDetail = fallbackStep.ReasonDetail
		}
		if (len(routes) == 0 || shouldFallbackImageChannelToFree(lastErr)) && h.Runner != nil && req != nil && len(policy.RunnerPlans) > 0 {
			trace.MarkFallback(fallbackStep, fallbackCode, fallbackDetail)
			h.persistRequestTrace(c.Request.Context(), req, trace)
			logFields := []zap.Field{zap.String("model", m.Slug)}
			if lastErr != nil {
				logFields = append(logFields, zap.Error(lastErr))
			} else {
				logFields = append(logFields, zap.String("reason", fallbackDetail))
			}
			logger.L().Warn("channel chat image fallback to account runner", logFields...)
			refund(imagepkg.ErrUpstream)
			req.runnerFallbackPlans = cloneImageRunnerFallbackPlans(policy.RunnerPlans)
			req.freeFallback = isFreeRunnerPlan(firstImageRunnerFallbackPlan(req.runnerFallbackPlans))
			if lastErr != nil {
				req.freeFallbackDetail = lastErr.Error()
			} else {
				req.freeFallbackDetail = fallbackDetail
			}
			return false
		}
		failure := imageChannelFailureFromErr(lastErr)
		if h.DAO != nil && req.taskID != "" {
			_ = h.DAO.MarkFailedDetail(c.Request.Context(), req.taskID, failure.Code, failure.Detail)
		}
		imagepkg.LogTaskLifecycle(req.taskID, trace, imagepkg.StatusFailed, failure.Code,
			zap.String("mode", "chat_image_channel"),
		)
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
	if h.DAO != nil && req.taskID != "" {
		_ = h.DAO.MarkSuccess(c.Request.Context(), req.taskID, "", nil, imageChannelResultURLs(result), finalCost)
		h.persistRequestTrace(c.Request.Context(), req, trace)
	}
	imagepkg.LogTaskLifecycle(req.taskID, trace, imagepkg.StatusSuccess, "",
		zap.String("mode", "chat_image_channel"),
	)

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
		MaskURL:           req.MaskURL,
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

func imageChannelFallbackRunOptions(job imageChannelAsyncJob) imagepkg.RunOptions {
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
		Trace:             job.ProviderTrace,
	}
	return opt
}

func imageChannelFreeFallbackRunOptions(job imageChannelAsyncJob) imagepkg.RunOptions {
	opt := imageChannelFallbackRunOptions(job)
	applyFreeFallbackPlan(&opt, true)
	return opt
}

func imageChannelAsyncPerAttemptTimeout(hasReferences bool) time.Duration {
	if hasReferences {
		return 2 * time.Minute
	}
	return 90 * time.Second
}

func imageChannelAsyncRouteTimeout(hasReferences bool) time.Duration {
	return imageChannelAsyncPerAttemptTimeout(hasReferences)
}

func imageChannelRouteTimeout(rt *channel.Route, hasReferences bool) time.Duration {
	timeout := imageChannelAsyncRouteTimeout(hasReferences)
	if rt == nil || rt.Channel == nil || rt.Channel.TimeoutS <= 0 {
		return timeout
	}
	configured := time.Duration(rt.Channel.TimeoutS) * time.Second
	if configured > timeout {
		return configured
	}
	return timeout
}

func imageChannelAsyncTimeout(routeCount int, hasReferences bool) time.Duration {
	if routeCount <= 0 {
		routeCount = 1
	}
	return time.Duration(routeCount)*imageChannelAsyncRouteTimeout(hasReferences) + 30*time.Second
}

func imageChannelRoutesTimeout(routes []*channel.Route, hasReferences bool) time.Duration {
	if len(routes) == 0 {
		return imageChannelAsyncTimeout(1, hasReferences)
	}
	total := 30 * time.Second
	for _, rt := range routes {
		total += imageChannelRouteTimeout(rt, hasReferences)
	}
	return total
}

func imageChannelTaskTimeout(hasReferences bool) time.Duration {
	return asyncImageTaskTimeout(0, hasReferences)
}

func imageChannelTaskTimeoutForRoutes(routes []*channel.Route, hasReferences bool) time.Duration {
	if len(routes) == 0 {
		return imageChannelTaskTimeout(hasReferences)
	}
	return imageChannelRoutesTimeout(routes, hasReferences) + imageChannelTaskTimeout(hasReferences)
}

func withImageChannelFallbackContext(parent context.Context, maxAttempts int, hasReferences bool) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, asyncImageTaskTimeout(maxAttempts, hasReferences))
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
		result, err := imageChannelGenerateAttempt(attemptCtx, rt, req)
		cancel()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempt >= maxAttempts || !shouldRetrySameImageChannel(err) {
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

func imageChannelGenerateAttempt(ctx context.Context, rt *channel.Route, req *adapter.ImageRequest) (*adapter.ImageResult, error) {
	type attemptResult struct {
		result *adapter.ImageResult
		err    error
	}
	done := make(chan attemptResult, 1)
	go func() {
		r, err := rt.Adapter.ImageGenerate(ctx, rt.UpstreamModel, req)
		done <- attemptResult{result: r, err: err}
	}()
	select {
	case r := <-done:
		return r.result, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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

func shouldRetrySameImageChannel(err error) bool {
	if !isRetryableImageChannelError(err) {
		return false
	}
	return !isImageChannelTimeoutError(err)
}

func isImageChannelTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var upstream *adapter.UpstreamHTTPError
	if errors.As(err, &upstream) && upstream.Status == http.StatusRequestTimeout {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "timeout")
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
