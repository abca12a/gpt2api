package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/432539/gpt2api/internal/channel"
	"github.com/432539/gpt2api/internal/upstream/adapter"
)

type stubImageChannelAdapter struct {
	calls int
	steps []stubImageChannelStep
}

type stubImageChannelStep struct {
	result *adapter.ImageResult
	err    error
}

func (s *stubImageChannelAdapter) Type() string { return "stub" }

func (s *stubImageChannelAdapter) Chat(context.Context, string, *adapter.ChatRequest) (adapter.ChatStream, error) {
	return nil, nil
}

func (s *stubImageChannelAdapter) ImageGenerate(context.Context, string, *adapter.ImageRequest) (*adapter.ImageResult, error) {
	s.calls++
	if len(s.steps) == 0 {
		return nil, nil
	}
	idx := s.calls - 1
	if idx >= len(s.steps) {
		idx = len(s.steps) - 1
	}
	return s.steps[idx].result, s.steps[idx].err
}

func (s *stubImageChannelAdapter) Ping(context.Context) error { return nil }

func TestImageChannelGenerateWithRetryRetriesTransientUpstreamDisconnect(t *testing.T) {
	rt := &channel.Route{
		Channel:       &channel.Channel{ID: 1, Name: "codex-cli-proxy-image"},
		UpstreamModel: "gpt-image-2",
		Adapter: &stubImageChannelAdapter{steps: []stubImageChannelStep{
			{err: &adapter.UpstreamHTTPError{Status: http.StatusBadGateway, Code: "internal_server_error", Type: "server_error", Message: "stream disconnected before completion"}},
			{result: &adapter.ImageResult{B64s: []string{"abc"}}},
		}},
	}

	got, err := imageChannelGenerateWithRetry(context.Background(), rt, &adapter.ImageRequest{Prompt: "draw"}, "img_retry", func(context.Context, time.Duration) error {
		return nil
	})
	if err != nil {
		t.Fatalf("imageChannelGenerateWithRetry() error = %v, want nil", err)
	}
	if got == nil || len(got.B64s) != 1 || got.B64s[0] != "abc" {
		t.Fatalf("unexpected result: %#v", got)
	}
	if stub := rt.Adapter.(*stubImageChannelAdapter); stub.calls != 2 {
		t.Fatalf("adapter calls = %d, want 2", stub.calls)
	}
}

func TestImageChannelGenerateWithRetryDoesNotRetryClientError(t *testing.T) {
	rt := &channel.Route{
		Channel:       &channel.Channel{ID: 1, Name: "codex-cli-proxy-image"},
		UpstreamModel: "gpt-image-2",
		Adapter: &stubImageChannelAdapter{steps: []stubImageChannelStep{
			{err: &adapter.UpstreamHTTPError{Status: http.StatusBadRequest, Code: "invalid_value", Type: "image_generation_user_error", Message: "Invalid size"}},
			{result: &adapter.ImageResult{B64s: []string{"should-not-retry"}}},
		}},
	}

	_, err := imageChannelGenerateWithRetry(context.Background(), rt, &adapter.ImageRequest{Prompt: "draw"}, "img_no_retry", func(context.Context, time.Duration) error {
		return nil
	})
	if err == nil {
		t.Fatal("imageChannelGenerateWithRetry() error = nil, want upstream 400")
	}
	if stub := rt.Adapter.(*stubImageChannelAdapter); stub.calls != 1 {
		t.Fatalf("adapter calls = %d, want 1", stub.calls)
	}
}

func TestIsRetryableImageChannelError(t *testing.T) {
	if !isRetryableImageChannelError(&adapter.UpstreamHTTPError{Status: http.StatusBadGateway, Code: "internal_server_error", Type: "server_error", Message: "stream disconnected before completion"}) {
		t.Fatal("502 stream disconnect should be retryable")
	}
	if isRetryableImageChannelError(&adapter.UpstreamHTTPError{Status: http.StatusBadRequest, Code: "invalid_value", Type: "image_generation_user_error", Message: "Invalid size"}) {
		t.Fatal("400 invalid_value should not be retryable")
	}
}
