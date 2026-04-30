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

func TestRunCapsSingleRequestToRequestedN(t *testing.T) {
	r := &Runner{
		runOnceHook: func(ctx context.Context, opt RunOptions, result *RunResult) (bool, string, error) {
			result.AccountID = 42
			result.ConversationID = "conv_1"
			result.FileIDs = []string{"file_1", "file_2"}
			result.SignedURLs = []string{"https://example.test/1.png", "https://example.test/2.png"}
			result.ContentTypes = []string{"image/png", "image/png"}
			return true, "", nil
		},
	}

	result := r.Run(context.Background(), RunOptions{N: 1, MaxAttempts: 1})
	if result.Status != StatusSuccess {
		t.Fatalf("status = %q, want success", result.Status)
	}
	if len(result.FileIDs) != 1 || result.FileIDs[0] != "file_1" {
		t.Fatalf("file ids = %#v, want only first image", result.FileIDs)
	}
	if len(result.SignedURLs) != 1 || result.SignedURLs[0] != "https://example.test/1.png" {
		t.Fatalf("signed urls = %#v, want only first image", result.SignedURLs)
	}
	if len(result.ContentTypes) != 1 || result.ContentTypes[0] != "image/png" {
		t.Fatalf("content types = %#v, want only first image", result.ContentTypes)
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

func TestReferenceUploadEOFIsNetworkTransient(t *testing.T) {
	err := errors.New(`upload reference 1: upload PUT: Put "https://sdmntprwestus3.oaiusercontent.com/raw": utls handshake sdmntprwestus3.oaiusercontent.com: EOF`)
	if got := classifyReferenceUploadError(err); got != ErrNetworkTransient {
		t.Fatalf("classifyReferenceUploadError = %q, want %q", got, ErrNetworkTransient)
	}
}

func TestReferenceUploadBadRequestStaysUpstream(t *testing.T) {
	err := &chatgpt.UpstreamError{Status: 400, Message: "upload PUT failed", Body: "invalid image"}
	if got := classifyReferenceUploadError(err); got != ErrUpstream {
		t.Fatalf("classifyReferenceUploadError = %q, want %q", got, ErrUpstream)
	}
}

func TestFilterOutReferenceFileIDsKeepsGeneratedImages(t *testing.T) {
	referenceSet := referenceUploadFileIDSet([]*chatgpt.UploadedFile{
		{FileID: "file_reference"},
		{FileID: "sed:legacy_reference"},
	})

	got := filterOutReferenceFileIDs([]string{
		"file_reference",
		"file_generated",
		"sed:file_reference",
		"legacy_reference",
		"sed:file_generated",
	}, referenceSet)
	want := []string{"file_generated", "sed:file_generated"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("filtered refs = %#v, want %#v", got, want)
	}
}

func TestShouldSkipPollRequiresGeneratedRefsNotUploadedReferences(t *testing.T) {
	referenceSet := referenceUploadFileIDSet([]*chatgpt.UploadedFile{
		{FileID: "file_ref_1"},
		{FileID: "file_ref_2"},
		{FileID: "file_ref_3"},
	})

	if shouldSkipImagePoll([]string{"file_ref_1", "sed:file_ref_2", "file_ref_3"}, referenceSet, 1) {
		t.Fatal("uploaded reference file IDs must not satisfy generated image count")
	}
	if !shouldSkipImagePoll([]string{"file_generated"}, referenceSet, 1) {
		t.Fatal("one generated image should satisfy n=1")
	}
}
