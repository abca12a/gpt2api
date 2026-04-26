package image

import (
	"strings"
	"testing"
)

func TestFormatTaskErrorPreservesCodeAndDetail(t *testing.T) {
	got := FormatTaskError(ErrUpstream, "upstream 502:\nstream disconnected before completion")
	if got != "upstream_error: upstream 502: stream disconnected before completion" {
		t.Fatalf("FormatTaskError() = %q", got)
	}
}

func TestSplitTaskErrorParsesFormattedAndLegacyUpstream(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		wantCode   string
		wantDetail string
	}{
		{name: "formatted", value: "upstream_error: upstream 502: stream disconnected", wantCode: ErrUpstream, wantDetail: "upstream 502: stream disconnected"},
		{name: "code only", value: ErrPollTimeout, wantCode: ErrPollTimeout},
		{name: "legacy upstream", value: "upstream 400: invalid size", wantCode: ErrUpstream, wantDetail: "upstream 400: invalid size"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, detail := SplitTaskError(tt.value)
			if code != tt.wantCode || detail != tt.wantDetail {
				t.Fatalf("SplitTaskError() = (%q, %q), want (%q, %q)", code, detail, tt.wantCode, tt.wantDetail)
			}
		})
	}
}

func TestFormatTaskErrorDefaultsEmptyCode(t *testing.T) {
	got := FormatTaskError("", "detail")
	if !strings.HasPrefix(got, ErrUnknown+": ") {
		t.Fatalf("empty code should default to unknown, got %q", got)
	}
}

func TestTaskErrorFieldsUsesAssistantDiagnosticForMessage(t *testing.T) {
	stored := FormatTaskError(ErrContentModeration, `poll error; assistant: I cannot help create that image; last_error: upstream returned error`)
	code, detail, message := TaskErrorFields(stored)
	if code != ErrContentModeration {
		t.Fatalf("code = %q, want %q", code, ErrContentModeration)
	}
	if !strings.Contains(detail, "last_error:") {
		t.Fatalf("detail should preserve raw diagnostics, got %q", detail)
	}
	if !strings.Contains(message, "上游说明:I cannot help create that image") {
		t.Fatalf("message should expose assistant diagnostic, got %q", message)
	}
}
