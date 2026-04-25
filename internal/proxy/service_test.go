package proxy

import "testing"

func TestFirstUsableProxySkipsDisabledAndZeroScore(t *testing.T) {
	proxies := []*Proxy{
		{ID: 1, Enabled: false, HealthScore: 100},
		{ID: 2, Enabled: true, HealthScore: 0},
		{ID: 3, Enabled: true, HealthScore: 20},
	}

	got := firstUsableProxy(proxies)
	if got == nil || got.ID != 3 {
		t.Fatalf("firstUsableProxy ID = %v, want 3", got)
	}
}

func TestFirstUsableProxyReturnsNilWhenNoneUsable(t *testing.T) {
	proxies := []*Proxy{
		nil,
		{ID: 1, Enabled: false, HealthScore: 100},
		{ID: 2, Enabled: true, HealthScore: 0},
	}

	if got := firstUsableProxy(proxies); got != nil {
		t.Fatalf("firstUsableProxy = %#v, want nil", got)
	}
}
