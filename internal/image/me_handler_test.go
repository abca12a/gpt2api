package image

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTaskViewIncludesUserVisibleErrorFields(t *testing.T) {
	task := &Task{
		TaskID: "img_failed",
		Status: StatusFailed,
		Error: FormatTaskError(
			ErrContentModeration,
			`poll error; assistant: I cannot help create that image; last_error: upstream returned error`,
		),
		CreatedAt: time.Unix(1777040000, 0),
	}

	view := toView(task)
	if view.ErrorCode != ErrContentModeration {
		t.Fatalf("ErrorCode = %q, want %q", view.ErrorCode, ErrContentModeration)
	}
	if !strings.Contains(view.ErrorMessage, "上游说明:I cannot help create that image") {
		t.Fatalf("ErrorMessage should expose assistant reason, got %q", view.ErrorMessage)
	}
	if !strings.Contains(view.ErrorDetail, "last_error:") {
		t.Fatalf("ErrorDetail should preserve raw diagnostics, got %q", view.ErrorDetail)
	}
}

func TestTaskViewIncludesProviderTraceSummary(t *testing.T) {
	traceJSON, err := json.Marshal(&TaskTrace{
		Original: TaskTraceEndpoint{
			Provider:    TraceProviderCodex,
			ChannelName: "codex-cli-proxy-image",
		},
		Final: TaskTraceEndpoint{
			Provider:        TraceProviderFreeRunner,
			AccountID:       9,
			AccountPlanType: "free",
			Status:          StatusSuccess,
		},
		Steps: []TaskTraceStep{
			{Order: 1, Provider: TraceProviderCodex, ChannelName: "codex-cli-proxy-image", Status: StatusFailed},
			{Order: 2, Provider: TraceProviderFreeRunner, AccountID: 9, AccountPlanType: "free", Status: StatusSuccess},
		},
	})
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}

	view := toView(&Task{
		TaskID:        "img_trace",
		Status:        StatusSuccess,
		ProviderTrace: traceJSON,
		CreatedAt:     time.Unix(1777040000, 0),
	})

	if view.ProviderTrace == nil {
		t.Fatal("ProviderTrace should be exposed to current user")
	}
	if !strings.Contains(view.ProviderTraceSummary, "Codex") || !strings.Contains(view.ProviderTraceSummary, "Free Runner(#9/free)") {
		t.Fatalf("unexpected ProviderTraceSummary: %q", view.ProviderTraceSummary)
	}
	if view.ProviderTrace.Final.AccountID != 9 {
		t.Fatalf("final account id = %d, want 9", view.ProviderTrace.Final.AccountID)
	}
}
