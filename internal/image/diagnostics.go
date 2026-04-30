package image

import (
	"time"

	"go.uber.org/zap"

	"github.com/432539/gpt2api/pkg/logger"
)

func LogTaskStage(taskID, phase string, duration time.Duration, fields ...zap.Field) {
	if duration <= 0 {
		return
	}
	baseFields := []zap.Field{
		zap.String("task_id", taskID),
		zap.String("phase", phase),
		zap.Duration("duration", duration),
	}
	baseFields = append(baseFields, fields...)
	if IsSlowTaskPhase(phase, duration) {
		logger.L().Warn("slow image task stage", baseFields...)
		return
	}
	logger.L().Info("image task stage", baseFields...)
}

func LogTaskLifecycle(taskID string, trace *TaskTrace, status, errorCode string, fields ...zap.Field) {
	if taskID == "" {
		return
	}
	timing := TaskTimingBreakdownFromTrace(trace)
	baseFields := []zap.Field{
		zap.String("task_id", taskID),
		zap.String("status", status),
		zap.Int64("request_ms", timing.RequestMs),
		zap.Int64("queue_ms", timing.QueueMs),
		zap.Int64("submit_ms", timing.SubmitMs),
		zap.Int64("upstream_wait_ms", timing.UpstreamWaitMs),
		zap.Int64("poll_ms", timing.PollMs),
		zap.Int64("download_ms", timing.DownloadMs),
		zap.Int64("total_ms", timing.TotalMs),
		zap.String("dominant_phase", timing.DominantPhase),
		zap.Int64("dominant_ms", timing.DominantMs),
	}
	if summary := TaskTraceSummary(trace); summary != "" {
		baseFields = append(baseFields, zap.String("provider_trace", summary))
	}
	if trace != nil {
		if trace.RequestID != "" {
			baseFields = append(baseFields, zap.String("request_id", trace.RequestID))
		}
		if trace.UpstreamRequestID != "" {
			baseFields = append(baseFields, zap.String("upstream_request_id", trace.UpstreamRequestID))
		}
		if trace.DownstreamStatus != "" {
			baseFields = append(baseFields, zap.String("downstream_status", trace.DownstreamStatus))
		}
		if trace.ErrorLayer != "" {
			baseFields = append(baseFields,
				zap.String("error_layer", trace.ErrorLayer),
				zap.String("error_layer_label", trace.ErrorLayerLabel),
			)
		}
	}
	if errorCode != "" {
		baseFields = append(baseFields, zap.String("error_code", errorCode))
	}
	baseFields = append(baseFields, fields...)
	if timing.TotalMs >= DefaultSlowTaskThreshold().Milliseconds() ||
		(timing.DominantMs > 0 && IsSlowTaskPhase(timing.DominantPhase, time.Duration(timing.DominantMs)*time.Millisecond)) {
		logger.L().Warn("slow image task overview", baseFields...)
		return
	}
	logger.L().Info("image task overview", baseFields...)
}

func logImageTaskStage(taskID, phase string, duration time.Duration, fields ...zap.Field) {
	LogTaskStage(taskID, phase, duration, fields...)
}
