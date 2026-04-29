package gateway

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/internal/apikey"
	"github.com/432539/gpt2api/internal/channel"
	imagepkg "github.com/432539/gpt2api/internal/image"
	modelpkg "github.com/432539/gpt2api/internal/model"
)

func imageProviderForRoute(rt *channel.Route) string {
	if rt == nil || rt.Channel == nil {
		return imagepkg.TraceProviderUnknown
	}
	name := strings.ToLower(strings.TrimSpace(rt.Channel.Name))
	baseURL := strings.ToLower(strings.TrimSpace(rt.Channel.BaseURL))
	switch {
	case strings.Contains(name, "codex") || strings.Contains(baseURL, "cli-proxy-api"):
		return imagepkg.TraceProviderCodex
	case strings.Contains(name, "apimart") || strings.Contains(baseURL, "apimart.ai"):
		return imagepkg.TraceProviderAPIMart
	case rt.Channel.Type == channel.TypeGemini:
		return imagepkg.TraceProviderGemini
	case rt.Channel.Type == channel.TypeOpenAI:
		return imagepkg.TraceProviderOpenAI
	default:
		return imagepkg.TraceProviderUnknown
	}
}

func runnerTraceProvider(requireFree bool) string {
	if requireFree {
		return imagepkg.TraceProviderFreeRunner
	}
	return imagepkg.TraceProviderAccountRunner
}

func imageTraceStepForRoute(rt *channel.Route) imagepkg.TaskTraceStep {
	step := imagepkg.TaskTraceStep{Provider: imagepkg.TraceProviderUnknown}
	if rt == nil || rt.Channel == nil {
		return step
	}
	step.Provider = imageProviderForRoute(rt)
	step.ChannelID = rt.Channel.ID
	step.ChannelName = rt.Channel.Name
	return step
}

func imageTraceStepForRunner(result *imagepkg.RunResult, requireFree bool, status, reasonCode, reasonDetail string) imagepkg.TaskTraceStep {
	step := imagepkg.TaskTraceStep{
		Provider:     runnerTraceProvider(requireFree),
		Status:       status,
		ReasonCode:   strings.TrimSpace(reasonCode),
		ReasonDetail: strings.TrimSpace(reasonDetail),
	}
	if result != nil {
		step.AccountID = result.AccountID
		step.AccountPlanType = result.AccountPlanType
	}
	return step
}

func ensureRequestTrace(req *ImageGenRequest) *imagepkg.TaskTrace {
	if req == nil {
		return nil
	}
	if req.providerTrace == nil {
		req.providerTrace = &imagepkg.TaskTrace{}
	}
	return req.providerTrace
}

func (h *ImagesHandler) persistRequestTrace(ctx context.Context, req *ImageGenRequest, trace *imagepkg.TaskTrace) {
	if req == nil {
		return
	}
	req.providerTrace = trace
	if h == nil || h.DAO == nil || req.taskID == "" {
		return
	}
	_ = h.DAO.UpdateProviderTrace(ctx, req.taskID, trace)
}

func (h *ImagesHandler) ensureTaskRecord(
	c *gin.Context,
	ak *apikey.APIKey,
	m *modelpkg.Model,
	req *ImageGenRequest,
	estimatedCost int64,
	trace *imagepkg.TaskTrace,
) (string, error) {
	if req == nil {
		return "", nil
	}
	req.providerTrace = trace
	if req.taskID != "" || h == nil || h.DAO == nil {
		return req.taskID, nil
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
		Upscale:         req.Upscale,
		Status:          imagepkg.StatusDispatched,
		EstimatedCredit: estimatedCost,
		ProviderTrace:   imagepkg.EncodeProviderTrace(trace),
	}
	downstreamUserInfoForTask(c, ak, req.User).applyToTask(task)
	if err := h.DAO.Create(c.Request.Context(), task); err != nil {
		return "", err
	}
	req.taskID = taskID
	return taskID, nil
}
