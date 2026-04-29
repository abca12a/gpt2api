package image

import "time"

const (
	TaskPhaseUnknown      = "unknown"
	TaskPhaseQueueWait    = "queue_wait"
	TaskPhaseSubmit       = "submit_upstream"
	TaskPhaseUpstreamWait = "upstream_wait"
	TaskPhaseTaskPoll     = "task_poll"
	TaskPhaseDownload     = "download_result"
)

const (
	defaultSlowTaskThreshold      = 90 * time.Second
	slowTaskQueueThreshold        = 10 * time.Second
	slowTaskSubmitThreshold       = 15 * time.Second
	slowTaskUpstreamWaitThreshold = 45 * time.Second
	slowTaskPollThreshold         = 30 * time.Second
	slowTaskDownloadThreshold     = 10 * time.Second
)

type TaskTraceTiming struct {
	RequestMs      int64  `json:"request_ms,omitempty"`
	QueueMs        int64  `json:"queue_ms,omitempty"`
	SubmitMs       int64  `json:"submit_ms,omitempty"`
	UpstreamWaitMs int64  `json:"upstream_wait_ms,omitempty"`
	PollMs         int64  `json:"poll_ms,omitempty"`
	DownloadMs     int64  `json:"download_ms,omitempty"`
	TotalMs        int64  `json:"total_ms,omitempty"`
	LastStatus     string `json:"last_status,omitempty"`
}

type TaskTimingBreakdown struct {
	RequestMs      int64  `json:"request_ms,omitempty"`
	QueueMs        int64  `json:"queue_ms,omitempty"`
	SubmitMs       int64  `json:"submit_ms,omitempty"`
	UpstreamWaitMs int64  `json:"upstream_wait_ms,omitempty"`
	PollMs         int64  `json:"poll_ms,omitempty"`
	DownloadMs     int64  `json:"download_ms,omitempty"`
	TotalMs        int64  `json:"total_ms,omitempty"`
	DominantPhase  string `json:"dominant_phase,omitempty"`
	DominantMs     int64  `json:"dominant_ms,omitempty"`
}

func DefaultSlowTaskThreshold() time.Duration { return defaultSlowTaskThreshold }

func SlowTaskPhaseThreshold(phase string) time.Duration {
	switch phase {
	case TaskPhaseQueueWait:
		return slowTaskQueueThreshold
	case TaskPhaseSubmit:
		return slowTaskSubmitThreshold
	case TaskPhaseUpstreamWait:
		return slowTaskUpstreamWaitThreshold
	case TaskPhaseTaskPoll:
		return slowTaskPollThreshold
	case TaskPhaseDownload:
		return slowTaskDownloadThreshold
	default:
		return defaultSlowTaskThreshold
	}
}

func IsSlowTaskPhase(phase string, d time.Duration) bool {
	return d >= SlowTaskPhaseThreshold(phase)
}

func (t *TaskTrace) ensureTiming() *TaskTraceTiming {
	if t == nil {
		return nil
	}
	if t.Timing == nil {
		t.Timing = &TaskTraceTiming{}
	}
	return t.Timing
}

func (t *TaskTrace) SetRequestDuration(d time.Duration) {
	if timing := t.ensureTiming(); timing != nil {
		timing.RequestMs = durationMillis(d)
	}
}

func (t *TaskTrace) SetQueueDuration(d time.Duration) {
	if timing := t.ensureTiming(); timing != nil {
		timing.QueueMs += durationMillis(d)
	}
}

func (t *TaskTrace) AddSubmitDuration(d time.Duration) {
	if timing := t.ensureTiming(); timing != nil {
		timing.SubmitMs += durationMillis(d)
	}
}

func (t *TaskTrace) AddUpstreamWaitDuration(d time.Duration) {
	if timing := t.ensureTiming(); timing != nil {
		timing.UpstreamWaitMs += durationMillis(d)
	}
}

func (t *TaskTrace) AddPollDuration(d time.Duration) {
	if timing := t.ensureTiming(); timing != nil {
		timing.PollMs += durationMillis(d)
	}
}

func (t *TaskTrace) AddDownloadDuration(d time.Duration) {
	if timing := t.ensureTiming(); timing != nil {
		timing.DownloadMs += durationMillis(d)
	}
}

func (t *TaskTrace) FinalizeTiming(status string, total time.Duration) {
	if timing := t.ensureTiming(); timing != nil {
		timing.TotalMs = durationMillis(total)
		timing.LastStatus = status
	}
}

func TaskTimingBreakdownFromTrace(trace *TaskTrace) TaskTimingBreakdown {
	if trace == nil || trace.Timing == nil {
		return TaskTimingBreakdown{}
	}
	out := TaskTimingBreakdown{
		RequestMs:      trace.Timing.RequestMs,
		QueueMs:        trace.Timing.QueueMs,
		SubmitMs:       trace.Timing.SubmitMs,
		UpstreamWaitMs: trace.Timing.UpstreamWaitMs,
		PollMs:         trace.Timing.PollMs,
		DownloadMs:     trace.Timing.DownloadMs,
		TotalMs:        trace.Timing.TotalMs,
	}
	out.DominantPhase, out.DominantMs = dominantTaskPhase(out)
	return out
}

func TaskTimingBreakdownFromTask(task *Task, now time.Time) TaskTimingBreakdown {
	if task == nil {
		return TaskTimingBreakdown{}
	}
	trace := task.DecodeProviderTrace()
	out := TaskTimingBreakdownFromTrace(trace)
	if out.QueueMs == 0 && task.StartedAt != nil && !task.StartedAt.IsZero() && task.StartedAt.After(task.CreatedAt) {
		out.QueueMs = task.StartedAt.Sub(task.CreatedAt).Milliseconds()
	}
	if out.TotalMs == 0 {
		switch {
		case task.FinishedAt != nil && !task.FinishedAt.IsZero():
			out.TotalMs = task.FinishedAt.Sub(task.CreatedAt).Milliseconds()
		case !task.CreatedAt.IsZero():
			out.TotalMs = now.Sub(task.CreatedAt).Milliseconds()
		}
	}
	if out.TotalMs < 0 {
		out.TotalMs = 0
	}
	out.DominantPhase, out.DominantMs = dominantTaskPhase(out)
	return out
}

func (t TaskTimingBreakdown) HasData() bool {
	return t.RequestMs > 0 || t.QueueMs > 0 || t.SubmitMs > 0 ||
		t.UpstreamWaitMs > 0 || t.PollMs > 0 || t.DownloadMs > 0 || t.TotalMs > 0
}

func dominantTaskPhase(t TaskTimingBreakdown) (string, int64) {
	phase := TaskPhaseUnknown
	maxMs := int64(0)
	candidates := []struct {
		phase string
		value int64
	}{
		{phase: TaskPhaseQueueWait, value: t.QueueMs},
		{phase: TaskPhaseSubmit, value: t.SubmitMs},
		{phase: TaskPhaseUpstreamWait, value: t.UpstreamWaitMs},
		{phase: TaskPhaseTaskPoll, value: t.PollMs},
		{phase: TaskPhaseDownload, value: t.DownloadMs},
	}
	for _, candidate := range candidates {
		if candidate.value <= maxMs {
			continue
		}
		phase = candidate.phase
		maxMs = candidate.value
	}
	return phase, maxMs
}

func durationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}
