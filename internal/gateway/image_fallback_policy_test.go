package gateway

import (
	"database/sql"
	"testing"
	"time"

	"github.com/432539/gpt2api/internal/channel"
	imagepkg "github.com/432539/gpt2api/internal/image"
)

func TestNormalizeRequestedImageResolutionDefaultsAndAliases(t *testing.T) {
	cases := []struct {
		name string
		req  *ImageGenRequest
		want string
	}{
		{name: "missing defaults to 1k", req: &ImageGenRequest{}, want: "1k"},
		{name: "resolution wins", req: &ImageGenRequest{Resolution: "2K", ImageSize: "4k"}, want: "2k"},
		{name: "image size alias", req: &ImageGenRequest{ImageSize: "2160p"}, want: "4k"},
		{name: "scale alias", req: &ImageGenRequest{Scale: "1440p"}, want: "2k"},
		{name: "quality legacy", req: &ImageGenRequest{Quality: "1024"}, want: "1k"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeRequestedImageResolution(tt.req); got != tt.want {
				t.Fatalf("resolution = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyRequestedImageResolutionStoresCanonicalValue(t *testing.T) {
	req := &ImageGenRequest{Resolution: "", ImageSize: "4K", Size: "16:9"}

	got := applyRequestedImageResolution(req)
	if got != "4k" {
		t.Fatalf("applied resolution = %q, want 4k", got)
	}
	if req.Resolution != "4k" {
		t.Fatalf("request resolution = %q, want canonical 4k", req.Resolution)
	}
}

func TestImageFallbackPolicyForResolution(t *testing.T) {
	base := defaultImageFallbackPolicy()

	oneK := imageFallbackPolicyForResolution(base, "1k")
	if len(oneK.ChannelOrder) != 0 {
		t.Fatalf("1k channel order = %#v, want no external channels", oneK.ChannelOrder)
	}
	if len(oneK.RunnerPlans) != 1 || !isFreeRunnerPlan(oneK.RunnerPlans[0]) {
		t.Fatalf("1k runner plans = %#v, want strict free", oneK.RunnerPlans)
	}

	twoK := imageFallbackPolicyForResolution(base, "2k")
	if len(twoK.ChannelOrder) != 2 || twoK.ChannelOrder[0] != imagepkg.TraceProviderCodex || twoK.ChannelOrder[1] != imagepkg.TraceProviderAPIMart {
		t.Fatalf("2k channel order = %#v, want codex->apimart", twoK.ChannelOrder)
	}
	if len(twoK.RunnerPlans) != 1 || !isFreeRunnerPlan(twoK.RunnerPlans[0]) {
		t.Fatalf("2k runner plans = %#v, want strict free fallback", twoK.RunnerPlans)
	}

	fourK := imageFallbackPolicyForResolution(base, "4k")
	if len(fourK.ChannelOrder) != 2 || fourK.ChannelOrder[0] != imagepkg.TraceProviderCodex || fourK.ChannelOrder[1] != imagepkg.TraceProviderAPIMart {
		t.Fatalf("4k channel order = %#v, want codex->apimart", fourK.ChannelOrder)
	}
	if len(fourK.RunnerPlans) != 1 || !isFreeRunnerPlan(fourK.RunnerPlans[0]) {
		t.Fatalf("4k runner plans = %#v, want strict free fallback", fourK.RunnerPlans)
	}
}

func TestPrepareImageRoutesFor1KSkipsExternalChannels(t *testing.T) {
	policy := imageFallbackPolicyForResolution(defaultImageFallbackPolicy(), "1k")
	codexRoute := &channel.Route{Channel: &channel.Channel{ID: 1, Name: "codex-cli-proxy-image", BaseURL: "http://cli-proxy-api:8317"}}
	apimartRoute := &channel.Route{Channel: &channel.Channel{ID: 2, Name: "apimart-image", BaseURL: "https://api.apimart.ai/v1"}}

	routes, skipped := prepareImageRoutes([]*channel.Route{codexRoute, apimartRoute}, policy)
	if len(routes) != 0 {
		t.Fatalf("routes = %#v, want no external routes for 1k", routes)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %#v, want both external channels skipped", skipped)
	}
	for _, step := range skipped {
		if step.ReasonCode != imageFallbackReasonResolutionRunnerOnly {
			t.Fatalf("skip reason = %q, want %q", step.ReasonCode, imageFallbackReasonResolutionRunnerOnly)
		}
	}
}

func TestEnsureRequestTraceStoresRequestedResolution(t *testing.T) {
	req := &ImageGenRequest{ImageSize: "4k"}

	trace := ensureRequestTrace(req)
	if trace == nil {
		t.Fatal("trace = nil")
	}
	if trace.Resolution != "4k" {
		t.Fatalf("trace resolution = %q, want 4k", trace.Resolution)
	}
	if req.Upscale != "" {
		t.Fatalf("request upscale = %q, should not be used for resolution storage", req.Upscale)
	}
}

func TestImageTaskResolutionReadsProviderTrace(t *testing.T) {
	task := &imagepkg.Task{ProviderTrace: imagepkg.EncodeProviderTrace(&imagepkg.TaskTrace{Resolution: "2k"})}

	if got := imageTaskResolution(task); got != "2k" {
		t.Fatalf("task resolution = %q, want 2k", got)
	}
}

func TestPrepareImageRoutesSkipsCodexWhenPolicyDirectsAPIMart(t *testing.T) {
	policy := imageFallbackPolicy{
		ChannelOrder:       []string{"apimart", "codex"},
		SkipCodexToAPIMart: true,
	}
	codexRoute := &channel.Route{
		Channel: &channel.Channel{ID: 1, Name: "codex-cli-proxy-image", BaseURL: "http://cli-proxy-api:8317"},
	}
	apimartRoute := &channel.Route{
		Channel: &channel.Channel{ID: 2, Name: "apimart-image", BaseURL: "https://api.apimart.ai/v1"},
	}

	routes, skipped := prepareImageRoutes([]*channel.Route{codexRoute, apimartRoute}, policy)
	if len(routes) != 1 || routes[0] != apimartRoute {
		t.Fatalf("routes = %#v, want only apimart", routes)
	}
	if len(skipped) != 1 || skipped[0].Provider != "codex" || skipped[0].ReasonCode != imageFallbackReasonSkipCodex {
		t.Fatalf("skipped = %#v, want codex skip step", skipped)
	}
}

func TestPrepareImageRoutesSkipsCoolingDownChannel(t *testing.T) {
	policy := imageFallbackPolicy{
		ChannelOrder:         []string{"codex", "apimart"},
		ChannelCooldown:      5 * time.Minute,
		ChannelFailThreshold: 3,
	}
	codexRoute := &channel.Route{
		Channel: &channel.Channel{
			ID:         1,
			Name:       "codex-cli-proxy-image",
			BaseURL:    "http://cli-proxy-api:8317",
			FailCount:  3,
			LastTestAt: sql.NullTime{Valid: true, Time: time.Now().Add(-2 * time.Minute)},
		},
	}
	apimartRoute := &channel.Route{
		Channel: &channel.Channel{ID: 2, Name: "apimart-image", BaseURL: "https://api.apimart.ai/v1"},
	}

	routes, skipped := prepareImageRoutes([]*channel.Route{codexRoute, apimartRoute}, policy)
	if len(routes) != 1 || routes[0] != apimartRoute {
		t.Fatalf("routes = %#v, want only apimart", routes)
	}
	if len(skipped) != 1 || skipped[0].ReasonCode != imageFallbackReasonChannelWarmup {
		t.Fatalf("skipped = %#v, want cooldown skip", skipped)
	}
}

func TestParseImageRunnerFallbackPlans(t *testing.T) {
	got := parseImageRunnerFallbackPlans([]string{"plus", "free", "any", "free"})
	if len(got) != 3 {
		t.Fatalf("len(plans) = %d, want 3", len(got))
	}
	if got[0].PreferredPlanType != "plus" || !got[0].RequirePlanType {
		t.Fatalf("first plan = %#v, want strict plus", got[0])
	}
	if got[1].PreferredPlanType != "free" || !got[1].RequirePlanType {
		t.Fatalf("second plan = %#v, want strict free", got[1])
	}
	if got[2].PreferredPlanType != "" || got[2].RequirePlanType {
		t.Fatalf("third plan = %#v, want any", got[2])
	}
}

func TestImageRouteMatchesPolicyToken(t *testing.T) {
	rt := &channel.Route{
		Channel: &channel.Channel{ID: 12, Name: "codex-cli-proxy-image", BaseURL: "http://cli-proxy-api:8317"},
	}
	cases := []struct {
		token string
		want  bool
	}{
		{token: "codex", want: true},
		{token: "provider:codex", want: true},
		{token: "codex-cli-proxy-image", want: true},
		{token: "channel:codex-cli-proxy-image", want: true},
		{token: "12", want: true},
		{token: "channel_id:12", want: true},
		{token: "apimart", want: false},
	}
	for _, tt := range cases {
		if got := imageRouteMatchesPolicyToken(rt, tt.token); got != tt.want {
			t.Fatalf("token %q => %v, want %v", tt.token, got, tt.want)
		}
	}
}
