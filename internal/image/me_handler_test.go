package image

import (
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
