package image

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/432539/gpt2api/internal/upstream/chatgpt"
)

func TestImagePollMaxWaitShortensMissingAcceptedTask(t *testing.T) {
	res := chatgpt.ImageSSEResult{}
	if got := imagePollMaxWait(res, nil, 60*time.Second); got != 20*time.Second {
		t.Fatalf("imagePollMaxWait = %s, want 20s", got)
	}
}

func TestImagePollMaxWaitKeepsAcceptedTaskWindow(t *testing.T) {
	res := chatgpt.ImageSSEResult{ImageGenTaskID: "chatimagegen-us-prod.task"}
	if got := imagePollMaxWait(res, nil, 60*time.Second); got != 60*time.Second {
		t.Fatalf("imagePollMaxWait = %s, want 60s", got)
	}
}

func TestImagePollMaxWaitKeepsExistingRefsWindow(t *testing.T) {
	res := chatgpt.ImageSSEResult{}
	if got := imagePollMaxWait(res, []string{"file_123"}, 60*time.Second); got != 60*time.Second {
		t.Fatalf("imagePollMaxWait = %s, want 60s", got)
	}
}

func TestImageSSEReadTimeout(t *testing.T) {
	if got := imageSSEReadTimeout(false); got != 30*time.Second {
		t.Fatalf("imageSSEReadTimeout(false) = %s, want 30s", got)
	}
	if got := imageSSEReadTimeout(true); got != 60*time.Second {
		t.Fatalf("imageSSEReadTimeout(true) = %s, want 60s", got)
	}
}

func TestRunParallelRetriesSubImageOnPollTimeout(t *testing.T) {
	var calls int32
	r := &Runner{
		runOnceHook: func(ctx context.Context, opt RunOptions, result *RunResult) (bool, string, error) {
			if atomic.AddInt32(&calls, 1) == 1 {
				return false, ErrPollTimeout, errors.New("poll timeout")
			}
			result.AccountID = 42
			result.ConversationID = "conv_1"
			result.FileIDs = []string{"file_1"}
			result.SignedURLs = []string{"https://example.test/image.png"}
			return true, "", nil
		},
	}
	result := &RunResult{Status: StatusFailed, ErrorCode: ErrUnknown}
	r.runParallel(context.Background(), RunOptions{
		TaskID:            "img_test",
		N:                 1,
		MaxAttempts:       2,
		PerAttemptTimeout: time.Second,
	}, time.Now(), result)

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("runOnce calls = %d, want 2", got)
	}
	if result.Status != StatusSuccess || len(result.FileIDs) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestImageFailureCodeFromAssistantDetectsModerationText(t *testing.T) {
	got := imageFailureCodeFromAssistant(ErrUpstream, "I can't help create that image because it may violate our safety policy.")
	if got != ErrContentModeration {
		t.Fatalf("code = %q, want %q", got, ErrContentModeration)
	}
}

func TestImageFailureErrorPreservesAssistantAndLastError(t *testing.T) {
	err := imageFailureError("poll error", "I cannot generate that image.", "conversation get failed")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "poll error") || !strings.Contains(msg, "assistant: I cannot generate") || !strings.Contains(msg, "last_error: conversation get failed") {
		t.Fatalf("unexpected diagnostic error: %q", msg)
	}
}
