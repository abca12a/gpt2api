package gateway

import (
	"net/http/httptest"
	"testing"
)

func TestDownstreamUserInfoFromTrustedHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/images/generations", nil)
	req.Header.Set(downstreamUserIDHeader, "1546")
	req.Header.Set(downstreamUsernameHeader, " mantou \n user ")
	req.Header.Set(downstreamUserEmailHeader, "Mantou@Example.COM")

	got := downstreamUserInfoFromRequest(req, "user_id=999;username=fallback", true)

	if got.ID != "1546" || got.Username != "mantou user" || got.Email != "mantou@example.com" {
		t.Fatalf("unexpected downstream user: %#v", got)
	}
	if got.Label != "#1546 / mantou user / mantou@example.com" {
		t.Fatalf("label = %q", got.Label)
	}
}

func TestDownstreamUserInfoIgnoresUntrustedHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/images/generations", nil)
	req.Header.Set(downstreamUserIDHeader, "1546")
	req.Header.Set(downstreamUsernameHeader, "mantou")

	got := downstreamUserInfoFromRequest(req, "user_id=1546;username=mantou", false)

	if got != (downstreamUserInfo{}) {
		t.Fatalf("untrusted request should not produce downstream user, got %#v", got)
	}
}

func TestDownstreamUserInfoFallsBackToUserField(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/images/generations", nil)

	got := downstreamUserInfoFromRequest(req, "user_id=1399;username=28;email=USER28@example.com", true)

	if got.ID != "1399" || got.Username != "28" || got.Email != "user28@example.com" {
		t.Fatalf("unexpected fallback user: %#v", got)
	}
	if got.Label != "#1399 / 28 / user28@example.com" {
		t.Fatalf("label = %q", got.Label)
	}
}

func TestIsTrustedDownstreamKeyID(t *testing.T) {
	if !isTrustedDownstreamKeyID(2, []uint64{1, 2, 3}) {
		t.Fatal("key id 2 should be trusted")
	}
	if isTrustedDownstreamKeyID(4, []uint64{1, 2, 3}) {
		t.Fatal("key id 4 should not be trusted")
	}
	if isTrustedDownstreamKeyID(2, nil) {
		t.Fatal("empty trust list should trust nobody")
	}
}
