package image

import (
	"testing"

	"github.com/432539/gpt2api/internal/upstream/chatgpt"
)

func TestShouldSkipImagePollWithoutAcceptedTask(t *testing.T) {
	res := chatgpt.ImageSSEResult{}
	if !shouldSkipImagePoll(res, nil) {
		t.Fatal("expected polling to be skipped when SSE has no image task and no refs")
	}
}

func TestShouldSkipImagePollAllowsAcceptedTask(t *testing.T) {
	res := chatgpt.ImageSSEResult{ImageGenTaskID: "chatimagegen-us-prod.task"}
	if shouldSkipImagePoll(res, nil) {
		t.Fatal("expected polling to continue when SSE accepted an image task")
	}
}

func TestShouldSkipImagePollAllowsExistingRefs(t *testing.T) {
	res := chatgpt.ImageSSEResult{}
	if shouldSkipImagePoll(res, []string{"file_123"}) {
		t.Fatal("expected polling to be skipped only when no refs exist")
	}
}
