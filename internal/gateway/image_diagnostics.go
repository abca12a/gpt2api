package gateway

import (
	"context"
	"time"

	"go.uber.org/zap"

	imagepkg "github.com/432539/gpt2api/internal/image"
	"github.com/432539/gpt2api/pkg/logger"
)

const (
	slowImageAsyncRequestThreshold = 1500 * time.Millisecond
	slowImageTaskQueryThreshold    = 800 * time.Millisecond
)

func (h *ImagesHandler) persistImageRequestTiming(ctx context.Context, req *ImageGenRequest, startedAt time.Time) {
	if h == nil || req == nil {
		return
	}
	trace := ensureRequestTrace(req)
	if trace == nil {
		return
	}
	trace.SetRequestDuration(time.Since(startedAt))
	h.persistRequestTrace(ctx, req, trace)
}

func logImageAsyncAccepted(taskID string, trace *imagepkg.TaskTrace, requestDuration time.Duration, referenceCount int, waitForResult bool) {
	if taskID == "" {
		return
	}
	fields := []zap.Field{
		zap.String("task_id", taskID),
		zap.Duration("request_duration", requestDuration),
		zap.Int("reference_count", referenceCount),
		zap.Bool("wait_for_result", waitForResult),
	}
	if summary := imagepkg.TaskTraceSummary(trace); summary != "" {
		fields = append(fields, zap.String("provider_trace", summary))
	}
	if requestDuration >= slowImageAsyncRequestThreshold {
		logger.L().Warn("slow image web request", fields...)
		return
	}
	logger.L().Info("image task accepted", fields...)
}

func logImageTaskQueryIfSlow(route string, task *imagepkg.Task, queryDuration time.Duration) {
	if task == nil || queryDuration < slowImageTaskQueryThreshold {
		return
	}
	timing := imagepkg.TaskTimingBreakdownFromTask(task, time.Now())
	fields := []zap.Field{
		zap.String("route", route),
		zap.String("task_id", task.TaskID),
		zap.String("status", task.Status),
		zap.Duration("query_duration", queryDuration),
		zap.Int64("task_total_ms", timing.TotalMs),
		zap.String("dominant_phase", timing.DominantPhase),
		zap.Int64("dominant_ms", timing.DominantMs),
	}
	if summary := imagepkg.TaskTraceSummary(task.DecodeProviderTrace()); summary != "" {
		fields = append(fields, zap.String("provider_trace", summary))
	}
	logger.L().Warn("slow image task query", fields...)
}
