package gateway

import (
	"database/sql"
	"testing"
	"time"

	"github.com/432539/gpt2api/internal/channel"
)

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
