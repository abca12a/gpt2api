package image

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/432539/gpt2api/internal/scheduler"
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
	if len(result.Parts) != 1 {
		t.Fatalf("parts = %#v, want one diagnostic part", result.Parts)
	}
	part := result.Parts[0]
	if part.Part != 1 || !part.OK || part.AccountID != 42 || part.ConversationID != "conv_1" || part.FileIDCount != 1 || part.SignedURLCount != 1 {
		t.Fatalf("unexpected part diagnostic: %#v", part)
	}
	if part.FirstFailure == nil || part.FirstFailure.ErrorCode != ErrPollTimeout {
		t.Fatalf("first failure = %#v, want poll timeout", part.FirstFailure)
	}
	if result.Merge == nil || !result.Merge.Complete || result.Merge.MergedFileIDCount != 1 || result.Merge.SucceededParts != 1 {
		t.Fatalf("unexpected merge diagnostic: %#v", result.Merge)
	}
}

func TestRunParallelDiagnosticsExposePartialMerge(t *testing.T) {
	var calls int32
	r := &Runner{
		runOnceHook: func(ctx context.Context, opt RunOptions, result *RunResult) (bool, string, error) {
			call := atomic.AddInt32(&calls, 1)
			if call == 1 {
				result.AccountID = 101
				result.AccountPlanType = "free"
				result.ConversationID = "conv_success"
				result.FileIDs = []string{"file_success"}
				result.SignedURLs = []string{"https://example.test/success.png"}
				return true, "", nil
			}
			result.AccountID = 202
			result.AccountPlanType = "free"
			result.ErrorMessage = fmt.Sprintf("part %d failed", call)
			return false, ErrUpstream, errors.New(result.ErrorMessage)
		},
	}
	result := &RunResult{Status: StatusFailed, ErrorCode: ErrUnknown}
	r.runParallel(context.Background(), RunOptions{
		TaskID:            "img_partial",
		N:                 2,
		MaxAttempts:       1,
		PerAttemptTimeout: time.Second,
	}, time.Now(), result)

	if result.Status != StatusSuccess {
		t.Fatalf("status = %q, want partial success", result.Status)
	}
	if len(result.Parts) != 2 {
		t.Fatalf("parts = %#v, want two diagnostics", result.Parts)
	}
	var okParts, failedParts int
	for _, part := range result.Parts {
		if part.OK {
			okParts++
			if part.AccountID != 101 || part.ConversationID != "conv_success" || part.FileIDCount != 1 {
				t.Fatalf("unexpected success part: %#v", part)
			}
			continue
		}
		failedParts++
		if part.AccountID != 202 || part.FirstFailure == nil || part.FirstFailure.ErrorCode != ErrUpstream || part.FinalErrorCode != ErrUpstream {
			t.Fatalf("unexpected failed part: %#v", part)
		}
	}
	if okParts != 1 || failedParts != 1 {
		t.Fatalf("ok parts=%d failed parts=%d, want 1/1", okParts, failedParts)
	}
	if result.Merge == nil || result.Merge.Complete || result.Merge.MissingImages != 1 || result.Merge.MergedFileIDCount != 1 || result.Merge.FailedParts != 1 {
		t.Fatalf("unexpected merge diagnostic: %#v", result.Merge)
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

func TestImageRunnerMarksUnauthorizedUpstreamAccountDead(t *testing.T) {
	for _, status := range []int{401, 403} {
		fake := &fakeImageScheduler{}
		r := &Runner{sched: fake}
		code := r.classifyUpstream(&chatgpt.UpstreamError{Status: status, Message: "unauthorized"})

		r.markAccountFailure(253, code)

		if fake.deadAccountID != 253 {
			t.Fatalf("status %d marked dead account %d, want 253", status, fake.deadAccountID)
		}
		if fake.rateLimitedAccountID != 0 {
			t.Fatalf("status %d also marked rate limited account %d", status, fake.rateLimitedAccountID)
		}
	}
}

func TestImageRunnerMarksRateLimitedAccountCooling(t *testing.T) {
	fake := &fakeImageScheduler{}
	r := &Runner{sched: fake}

	r.markAccountFailure(254, ErrRateLimited)

	if fake.rateLimitedAccountID != 254 {
		t.Fatalf("rate limited account = %d, want 254", fake.rateLimitedAccountID)
	}
	if fake.deadAccountID != 0 {
		t.Fatalf("rate limited account should not be marked dead, got %d", fake.deadAccountID)
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

type fakeImageScheduler struct {
	deadAccountID        uint64
	rateLimitedAccountID uint64
	warnedAccountID      uint64
}

func (f *fakeImageScheduler) Dispatch(context.Context, string) (*scheduler.Lease, error) {
	return nil, scheduler.ErrNoAvailable
}

func (f *fakeImageScheduler) DispatchWithPlan(context.Context, string, string, bool) (*scheduler.Lease, error) {
	return nil, scheduler.ErrNoAvailable
}

func (f *fakeImageScheduler) MarkRateLimited(_ context.Context, accountID uint64) {
	f.rateLimitedAccountID = accountID
}

func (f *fakeImageScheduler) MarkWarned(_ context.Context, accountID uint64) {
	f.warnedAccountID = accountID
}

func (f *fakeImageScheduler) MarkDead(_ context.Context, accountID uint64) {
	f.deadAccountID = accountID
}
