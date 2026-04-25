package image

import (
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
