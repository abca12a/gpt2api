package gateway

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/432539/gpt2api/internal/channel"
	imagepkg "github.com/432539/gpt2api/internal/image"
	modelpkg "github.com/432539/gpt2api/internal/model"
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

type blockingImageChannelAdapter struct {
	calls int
}

func (s *blockingImageChannelAdapter) Type() string { return "blocking" }

func (s *blockingImageChannelAdapter) Chat(context.Context, string, *adapter.ChatRequest) (adapter.ChatStream, error) {
	return nil, nil
}

func (s *blockingImageChannelAdapter) ImageGenerate(ctx context.Context, _ string, _ *adapter.ImageRequest) (*adapter.ImageResult, error) {
	s.calls++
	if s.calls == 1 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &adapter.ImageResult{B64s: []string{"retry-ok"}}, nil
}

func (s *blockingImageChannelAdapter) Ping(context.Context) error { return nil }

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

	got, err := imageChannelGenerateWithRetry(context.Background(), rt, &adapter.ImageRequest{Prompt: "draw"}, "img_retry", 0, func(context.Context, time.Duration) error {
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

	_, err := imageChannelGenerateWithRetry(context.Background(), rt, &adapter.ImageRequest{Prompt: "draw"}, "img_no_retry", 0, func(context.Context, time.Duration) error {
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
	if !isRetryableImageChannelError(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded should remain retryable for fallback classification")
	}
	if isRetryableImageChannelError(&adapter.UpstreamHTTPError{Status: http.StatusBadRequest, Code: "invalid_value", Type: "image_generation_user_error", Message: "Invalid size"}) {
		t.Fatal("400 invalid_value should not be retryable")
	}
}

func TestShouldRetrySameImageChannel(t *testing.T) {
	if !shouldRetrySameImageChannel(&adapter.UpstreamHTTPError{Status: http.StatusBadGateway, Code: "internal_server_error", Type: "server_error", Message: "stream disconnected before completion"}) {
		t.Fatal("502 stream disconnect should retry same channel")
	}
	if shouldRetrySameImageChannel(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded should not retry same channel")
	}
	if shouldRetrySameImageChannel(&adapter.UpstreamHTTPError{Status: http.StatusRequestTimeout, Message: "timeout"}) {
		t.Fatal("408 timeout should not retry same channel")
	}
	if shouldRetrySameImageChannel(&adapter.UpstreamHTTPError{Status: http.StatusBadRequest, Code: "invalid_value", Type: "image_generation_user_error", Message: "Invalid size"}) {
		t.Fatal("400 invalid_value should not retry same channel")
	}
}

func TestShouldFallbackImageChannelToFreeOn502(t *testing.T) {
	if !shouldFallbackImageChannelToFree(&adapter.UpstreamHTTPError{Status: http.StatusBadGateway, Message: "stream disconnected before completion"}) {
		t.Fatal("502 should trigger Free fallback")
	}
	if !shouldFallbackImageChannelToFree(&adapter.UpstreamHTTPError{Status: http.StatusInternalServerError, Message: "stream error: stream ID 37; INTERNAL_ERROR"}) {
		t.Fatal("500 stream errors should trigger Free fallback")
	}
	if !shouldFallbackImageChannelToFree(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded should trigger Free fallback")
	}
	if !shouldFallbackImageChannelToFree(errors.New("openai: image request: EOF")) {
		t.Fatal("EOF should trigger Free fallback")
	}
	if shouldFallbackImageChannelToFree(&adapter.UpstreamHTTPError{Status: http.StatusBadRequest, Code: "invalid_value", Message: "Invalid size"}) {
		t.Fatal("400 invalid value should not trigger Free fallback")
	}
	if shouldFallbackImageChannelToFree(&adapter.UpstreamHTTPError{Status: http.StatusBadRequest, Code: "content_policy_violation", Message: "blocked"}) {
		t.Fatal("content moderation should not trigger Free fallback")
	}
}

func TestImageChannelFreeFallbackRunOptionsRequireFreePlan(t *testing.T) {
	job := imageChannelAsyncJob{
		TaskID:  "img_free",
		UserID:  10,
		KeyID:   20,
		ModelID: 30,
		Model:   &modelpkg.Model{UpstreamModelSlug: "gpt-image-2"},
		Request: &adapter.ImageRequest{Prompt: "draw", N: 2},
		References: []imagepkg.ReferenceImage{
			{Data: []byte("ref"), FileName: "ref.png"},
		},
	}

	got := imageChannelFreeFallbackRunOptions(job)
	if got.PreferredPlanType != "free" || !got.RequirePlanType {
		t.Fatalf("fallback plan = %q strict=%v, want strict free", got.PreferredPlanType, got.RequirePlanType)
	}
	if got.TaskID != job.TaskID || got.UserID != job.UserID || got.KeyID != job.KeyID || got.ModelID != job.ModelID {
		t.Fatalf("run options lost task identity: %#v", got)
	}
	if got.Prompt != "draw" || got.N != 2 || got.UpstreamModel != "gpt-image-2" {
		t.Fatalf("unexpected request mapping: %#v", got)
	}
	if len(got.References) != 1 || string(got.References[0].Data) != "ref" {
		t.Fatalf("references not preserved: %#v", got.References)
	}
}

func TestImageChannelAsyncTimeoutCapsExternalChannelBeforeFallback(t *testing.T) {
	if got := imageChannelAsyncPerAttemptTimeout(false); got != 90*time.Second {
		t.Fatalf("no-reference async per-attempt timeout = %s, want 90s", got)
	}
	if got := imageChannelAsyncRouteTimeout(false); got != 90*time.Second {
		t.Fatalf("no-reference async route timeout = %s, want 90s", got)
	}
	if got := imageChannelAsyncTimeout(2, false); got != 3*time.Minute+30*time.Second {
		t.Fatalf("no-reference async timeout = %s, want 3m30s", got)
	}
	if got := imageChannelAsyncPerAttemptTimeout(true); got != 2*time.Minute {
		t.Fatalf("reference async per-attempt timeout = %s, want 2m", got)
	}
	if got := imageChannelAsyncRouteTimeout(true); got != 2*time.Minute {
		t.Fatalf("reference async route timeout = %s, want 2m", got)
	}
	if got := imageChannelAsyncTimeout(2, true); got != 4*time.Minute+30*time.Second {
		t.Fatalf("reference async timeout = %s, want 4m30s", got)
	}
	if got := imageChannelTaskTimeout(false); got != 8*time.Minute {
		t.Fatalf("no-reference task timeout = %s, want 8m", got)
	}
	if got := imageChannelTaskTimeout(true); got != 8*time.Minute+30*time.Second {
		t.Fatalf("reference task timeout = %s, want 8m30s", got)
	}
	if imageChannelAsyncTimeout(2, true) >= imageChannelTaskTimeout(true) {
		t.Fatalf("reference channel timeout %s should leave fallback reserve inside total task timeout %s", imageChannelAsyncTimeout(2, true), imageChannelTaskTimeout(true))
	}
}

func TestImageChannelRouteTimeoutRespectsChannelConfig(t *testing.T) {
	if got := imageChannelRouteTimeout(&channel.Route{Channel: &channel.Channel{TimeoutS: 120}}, false); got != 120*time.Second {
		t.Fatalf("route timeout = %s, want configured 120s", got)
	}
	if got := imageChannelRouteTimeout(&channel.Route{Channel: &channel.Channel{TimeoutS: 30}}, false); got != 90*time.Second {
		t.Fatalf("route timeout = %s, want async floor 90s", got)
	}
	if got := imageChannelRouteTimeout(&channel.Route{Channel: &channel.Channel{TimeoutS: 180}}, true); got != 180*time.Second {
		t.Fatalf("reference route timeout = %s, want configured 180s", got)
	}
	routes := []*channel.Route{
		{Channel: &channel.Channel{TimeoutS: 120}},
		{Channel: &channel.Channel{TimeoutS: 120}},
	}
	if got := imageChannelRoutesTimeout(routes, false); got != 4*time.Minute+30*time.Second {
		t.Fatalf("routes timeout = %s, want two configured routes plus reserve", got)
	}
	if got := imageChannelTaskTimeoutForRoutes(routes, false); got != 12*time.Minute+30*time.Second {
		t.Fatalf("task timeout = %s, want channel window plus fallback reserve", got)
	}
}

func TestImageChannelFallbackContextDoesNotExtendPastParentDeadline(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelParent()

	child, cancelChild := withImageChannelFallbackContext(parent, 2, true)
	defer cancelChild()

	parentDeadline, ok := parent.Deadline()
	if !ok {
		t.Fatal("parent should have deadline")
	}
	childDeadline, ok := child.Deadline()
	if !ok {
		t.Fatal("child should have deadline")
	}
	if childDeadline.After(parentDeadline) {
		t.Fatalf("child deadline %s should not extend past parent %s", childDeadline, parentDeadline)
	}
}

func TestLimitImageChannelResultCapsToRequestedN(t *testing.T) {
	result := &adapter.ImageResult{
		URLs: []string{"https://example.test/1.png", "https://example.test/2.png"},
		B64s: []string{"b64-1"},
	}

	got := limitImageChannelResult(result, 1)
	if got == result {
		t.Fatal("limitImageChannelResult should not mutate the original result")
	}
	if len(got.URLs) != 1 || got.URLs[0] != "https://example.test/1.png" || len(got.B64s) != 0 {
		t.Fatalf("limited result = %#v, want only first URL", got)
	}
	if len(result.URLs) != 2 || len(result.B64s) != 1 {
		t.Fatalf("original result mutated: %#v", result)
	}
}

func TestActualCountFallsBackToOneForEmptySuccessfulChannelResult(t *testing.T) {
	if got := actualCount(&adapter.ImageResult{}); got != 1 {
		t.Fatalf("actualCount(empty result) = %d, want 1", got)
	}
	if got := actualCount(&adapter.ImageResult{URLs: []string{"u1"}, B64s: []string{"b1"}}); got != 2 {
		t.Fatalf("actualCount(two images) = %d, want 2", got)
	}
}
func TestImageChannelGenerateWithRetryDoesNotRetryAttemptTimeout(t *testing.T) {
	rt := &channel.Route{
		Channel:       &channel.Channel{ID: 1, Name: "codex-cli-proxy-image"},
		UpstreamModel: "gpt-image-2",
		Adapter:       &blockingImageChannelAdapter{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	got, err := imageChannelGenerateWithRetry(ctx, rt, &adapter.ImageRequest{Prompt: "draw"}, "img_retry_timeout", 100*time.Millisecond, func(context.Context, time.Duration) error {
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("imageChannelGenerateWithRetry() error = %v, want deadline exceeded", err)
	}
	if got != nil {
		t.Fatalf("unexpected result: %#v", got)
	}
	if stub := rt.Adapter.(*blockingImageChannelAdapter); stub.calls != 1 {
		t.Fatalf("adapter calls = %d, want 1", stub.calls)
	}
}

func TestImageProviderForRouteDistinguishesCodexAndAPIMart(t *testing.T) {
	cases := []struct {
		name  string
		route *channel.Route
		want  string
	}{
		{
			name: "codex from channel name",
			route: &channel.Route{Channel: &channel.Channel{
				Name:    "codex-cli-proxy-image",
				BaseURL: "http://cli-proxy-api:8317",
				Type:    channel.TypeOpenAI,
			}},
			want: imagepkg.TraceProviderCodex,
		},
		{
			name: "apimart from base url",
			route: &channel.Route{Channel: &channel.Channel{
				Name:    "openai-image",
				BaseURL: "https://api.apimart.ai/v1",
				Type:    channel.TypeOpenAI,
			}},
			want: imagepkg.TraceProviderAPIMart,
		},
		{
			name: "gemini from channel type",
			route: &channel.Route{Channel: &channel.Channel{
				Name:    "imagen",
				BaseURL: "https://generativelanguage.googleapis.com",
				Type:    channel.TypeGemini,
			}},
			want: imagepkg.TraceProviderGemini,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := imageProviderForRoute(tt.route); got != tt.want {
				t.Fatalf("imageProviderForRoute() = %q, want %q", got, tt.want)
			}
		})
	}
}
