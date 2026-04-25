package oaierr

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestTypeForStatusUsesAPIMartErrorTypes(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, "invalid_request_error"},
		{http.StatusUnauthorized, "authentication_error"},
		{http.StatusPaymentRequired, "payment_required"},
		{http.StatusTooManyRequests, "rate_limit_error"},
		{http.StatusInternalServerError, "server_error"},
		{http.StatusServiceUnavailable, "service_unavailable"},
	}

	for _, tt := range tests {
		if got := TypeForStatus(tt.status); got != tt.want {
			t.Fatalf("TypeForStatus(%d) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestPayloadKeepsOpenAIStringCodeByDefault(t *testing.T) {
	got := Payload(http.StatusPaymentRequired, "insufficient_balance", "积分不足")
	if got.Error.Type != "payment_required" {
		t.Fatalf("type = %q, want payment_required", got.Error.Type)
	}
	if got.Error.Code != "insufficient_balance" {
		t.Fatalf("code = %#v, want string code", got.Error.Code)
	}
	if got.Error.Message != "积分不足" {
		t.Fatalf("message = %q", got.Error.Message)
	}
}

func TestAPIMartPayloadUsesHTTPStatusCodeAndDefaultMessage(t *testing.T) {
	got := APIMartPayload(http.StatusTooManyRequests, "")
	if got.Error.Type != "rate_limit_error" {
		t.Fatalf("type = %q, want rate_limit_error", got.Error.Type)
	}
	if got.Error.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %#v, want HTTP status code", got.Error.Code)
	}
	if got.Error.Message != "请求过于频繁，请稍后再试" {
		t.Fatalf("message = %q", got.Error.Message)
	}
}

func TestWantsAPIMartRecognizesQueryAndHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		target string
		header map[string]string
		want   bool
	}{
		{target: "/v1/images/generations", want: false},
		{target: "/v1/images/generations?compat=apimart", want: true},
		{target: "/v1/images/generations?response_schema=apimart", want: true},
		{target: "/v1/images/generations", header: map[string]string{"X-Response-Format": "apimart"}, want: true},
	}

	for _, tt := range tests {
		req, err := http.NewRequest(http.MethodPost, tt.target, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range tt.header {
			req.Header.Set(k, v)
		}
		c := &gin.Context{Request: req}
		if got := WantsAPIMart(c); got != tt.want {
			t.Fatalf("WantsAPIMart(%q, %#v) = %v, want %v", tt.target, tt.header, got, tt.want)
		}
	}
}
