package middleware

import "testing"

func TestSanitizeRawQueryForLogRedactsSensitiveValues(t *testing.T) {
	got := sanitizeRawQueryForLog("async=true&sig=abc123&exp=1777729279719&api_key=sk-live&access_token=tok&foo=bar")
	want := "access_token=REDACTED&api_key=REDACTED&async=true&exp=1777729279719&foo=bar&sig=REDACTED"
	if got != want {
		t.Fatalf("sanitized query = %q, want %q", got, want)
	}
}

func TestSanitizeRawQueryForLogHandlesMalformedQuery(t *testing.T) {
	raw := "sig=%zz&foo=bar"
	if got := sanitizeRawQueryForLog(raw); got != raw {
		t.Fatalf("malformed query = %q, want original %q", got, raw)
	}
}
