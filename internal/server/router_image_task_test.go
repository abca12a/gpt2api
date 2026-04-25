package server

import (
	"testing"

	"github.com/432539/gpt2api/internal/config"
	"github.com/432539/gpt2api/internal/gateway"
)

func TestImageTaskCompatibilityRouteRegistered(t *testing.T) {
	r := New(&Deps{
		Config:   &config.Config{},
		GatewayH: &gateway.Handler{},
		ImagesH:  &gateway.ImagesHandler{},
	})

	want := map[string]bool{
		"GET /v1/images/tasks/:id": false,
		"GET /v1/tasks/:id":        false,
	}
	for _, route := range r.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}

	for route, found := range want {
		if !found {
			t.Fatalf("expected route %s to be registered", route)
		}
	}
}
