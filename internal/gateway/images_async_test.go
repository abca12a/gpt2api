package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestShouldWaitForImageResultAsyncCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name   string
		target string
		header map[string]string
		req    ImageGenRequest
		want   bool
	}{
		{name: "default sync", target: "/v1/images/generations", want: true},
		{name: "query async true", target: "/v1/images/generations?async=true", want: false},
		{name: "query wait false", target: "/v1/images/generations?wait_for_result=false", want: false},
		{name: "prefer respond async", target: "/v1/images/generations", header: map[string]string{"Prefer": "respond-async"}, want: false},
		{name: "body wait false", target: "/v1/images/generations", req: ImageGenRequest{WaitForResult: boolPtr(false)}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			req := httptest.NewRequest(http.MethodPost, tt.target, nil)
			for k, v := range tt.header {
				req.Header.Set(k, v)
			}
			c.Request = req

			if got := shouldWaitForImageResult(c, tt.req); got != tt.want {
				t.Fatalf("shouldWaitForImageResult() = %v, want %v", got, tt.want)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }
