package image

import (
	"testing"
	"time"
)

func TestBuildProviderTraceStats(t *testing.T) {
	rows := []ProviderTraceStatRow{
		{
			Status: StatusSuccess,
			ProviderTrace: EncodeProviderTrace(&TaskTrace{
				Original: TaskTraceEndpoint{Provider: TraceProviderCodex, ChannelName: "codex-image"},
				Final:    TaskTraceEndpoint{Provider: TraceProviderCodex, ChannelName: "codex-image"},
				Steps: []TaskTraceStep{
					{Order: 1, Provider: TraceProviderCodex, ChannelName: "codex-image", Status: StatusSuccess},
				},
			}),
		},
		{
			Status: StatusSuccess,
			ProviderTrace: EncodeProviderTrace(&TaskTrace{
				Original: TaskTraceEndpoint{Provider: TraceProviderCodex, ChannelName: "codex-image"},
				Fallback: &TaskTraceFallback{
					Triggered:    true,
					FromProvider: TraceProviderCodex,
					ReasonCode:   ErrUpstream,
				},
				Final: TaskTraceEndpoint{Provider: TraceProviderAPIMart, ChannelName: "apimart-image"},
				Steps: []TaskTraceStep{
					{Order: 1, Provider: TraceProviderCodex, ChannelName: "codex-image", Status: StatusFailed, ReasonCode: ErrUpstream},
					{Order: 2, Provider: TraceProviderAPIMart, ChannelName: "apimart-image", Status: StatusSuccess},
				},
			}),
		},
		{
			Status: StatusSuccess,
			ProviderTrace: EncodeProviderTrace(&TaskTrace{
				Original: TaskTraceEndpoint{Provider: TraceProviderCodex, ChannelName: "codex-image"},
				Fallback: &TaskTraceFallback{
					Triggered:    true,
					FromProvider: TraceProviderCodex,
					ReasonCode:   ErrUpstream,
				},
				Final: TaskTraceEndpoint{Provider: TraceProviderFreeRunner, AccountID: 9, AccountPlanType: "free"},
				Steps: []TaskTraceStep{
					{Order: 1, Provider: TraceProviderCodex, ChannelName: "codex-image", Status: traceStepStatusSkipped, ReasonCode: "policy_skip_codex"},
					{Order: 2, Provider: TraceProviderFreeRunner, AccountID: 9, AccountPlanType: "free", Status: StatusSuccess},
				},
			}),
		},
	}

	stats := BuildProviderTraceStats(rows, 24)
	if stats.Total != 3 || stats.Success != 3 || stats.FallbackTriggered != 2 {
		t.Fatalf("unexpected summary: %#v", stats)
	}

	codex := findProviderStat(stats.Providers, TraceProviderCodex)
	if codex == nil {
		t.Fatal("codex stats missing")
	}
	if codex.FirstSelected != 2 {
		t.Fatalf("codex first_selected = %d, want 2", codex.FirstSelected)
	}
	if codex.FinalSelected != 1 {
		t.Fatalf("codex final_selected = %d, want 1", codex.FinalSelected)
	}
	if codex.Skipped != 1 {
		t.Fatalf("codex skipped = %d, want 1", codex.Skipped)
	}
	if codex.FallbackFrom != 2 {
		t.Fatalf("codex fallback_from = %d, want 2", codex.FallbackFrom)
	}

	apimart := findProviderStat(stats.Providers, TraceProviderAPIMart)
	if apimart == nil || apimart.FinalSelected != 1 {
		t.Fatalf("apimart stats = %#v, want final_selected=1", apimart)
	}
	free := findProviderStat(stats.Providers, TraceProviderFreeRunner)
	if free == nil || free.FinalSelected != 1 {
		t.Fatalf("free runner stats = %#v, want final_selected=1", free)
	}

	if len(stats.Transitions) != 2 {
		t.Fatalf("transitions len = %d, want 2", len(stats.Transitions))
	}
}

func findProviderStat(stats []ProviderHitStat, provider string) *ProviderHitStat {
	for i := range stats {
		if stats[i].Provider == provider {
			return &stats[i]
		}
	}
	return nil
}

func TestBuildProviderTraceStatsIncludesSlowTaskOverview(t *testing.T) {
	now := time.Unix(1_777_040_000, 0)
	rows := []ProviderTraceStatRow{
		{
			TaskID:     "img_wait",
			Status:     StatusSuccess,
			CreatedAt:  now.Add(-3 * time.Minute),
			StartedAt:  timePtr(now.Add(-170 * time.Second)),
			FinishedAt: timePtr(now.Add(-20 * time.Second)),
			ProviderTrace: EncodeProviderTrace(&TaskTrace{
				Final: TaskTraceEndpoint{Provider: TraceProviderCodex},
				Timing: &TaskTraceTiming{
					QueueMs:        10_000,
					SubmitMs:       8_000,
					UpstreamWaitMs: 120_000,
					TotalMs:        150_000,
				},
			}),
		},
		{
			TaskID:     "img_poll",
			Status:     StatusFailed,
			Error:      ErrPollTimeout,
			CreatedAt:  now.Add(-4 * time.Minute),
			StartedAt:  timePtr(now.Add(-220 * time.Second)),
			FinishedAt: timePtr(now.Add(-10 * time.Second)),
			ProviderTrace: EncodeProviderTrace(&TaskTrace{
				Final: TaskTraceEndpoint{Provider: TraceProviderAPIMart},
				Timing: &TaskTraceTiming{
					SubmitMs:       5_000,
					UpstreamWaitMs: 12_000,
					PollMs:         160_000,
					TotalMs:        177_000,
				},
			}),
		},
	}

	stats := BuildProviderTraceStatsWithOptions(rows, ProviderTraceStatsOptions{
		WindowHours:   24,
		SlowThreshold: 60 * time.Second,
		SlowLimit:     10,
		Now:           now,
	})
	if stats.Slow.Total != 2 {
		t.Fatalf("slow total = %d, want 2", stats.Slow.Total)
	}
	if len(stats.Slow.Tasks) != 2 {
		t.Fatalf("slow tasks len = %d, want 2", len(stats.Slow.Tasks))
	}
	if stats.Slow.Tasks[0].TaskID != "img_poll" || stats.Slow.Tasks[0].DominantPhase != TaskPhaseTaskPoll {
		t.Fatalf("top slow task = %#v, want poll-dominant img_poll", stats.Slow.Tasks[0])
	}
	if stats.Slow.Tasks[1].TaskID != "img_wait" || stats.Slow.Tasks[1].DominantPhase != TaskPhaseUpstreamWait {
		t.Fatalf("second slow task = %#v, want upstream-wait img_wait", stats.Slow.Tasks[1])
	}
	if len(stats.Slow.Phases) != 2 {
		t.Fatalf("slow phases len = %d, want 2", len(stats.Slow.Phases))
	}
}

func timePtr(v time.Time) *time.Time { return &v }
