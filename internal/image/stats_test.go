package image

import (
	"testing"
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
