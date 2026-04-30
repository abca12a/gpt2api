package image

import (
	"encoding/json"
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

func TestTaskTraceSummaryShowsFallbackOrderAndFinalAccount(t *testing.T) {
	trace := &TaskTrace{
		RequestID:         "req-123",
		TaskID:            "img_123",
		ErrorLayer:        ErrorLayerDownstreamAPIMart,
		ErrorLayerLabel:   ErrorLayerLabel(ErrorLayerDownstreamAPIMart),
		UpstreamRequestID: "apimart-task-123",
		Original: TaskTraceEndpoint{
			Provider:    TraceProviderCodex,
			ChannelID:   1,
			ChannelName: "codex-cli-proxy-image",
		},
		Fallback: &TaskTraceFallback{
			Triggered:    true,
			ReasonCode:   ErrUpstream,
			ReasonDetail: "upstream 502: stream disconnected before completion",
			FromProvider: TraceProviderAPIMart,
		},
		Final: TaskTraceEndpoint{
			Provider:        TraceProviderFreeRunner,
			AccountID:       42,
			AccountPlanType: "free",
			Status:          StatusSuccess,
		},
		Steps: []TaskTraceStep{
			{
				Order:       1,
				Provider:    TraceProviderCodex,
				ChannelID:   1,
				ChannelName: "codex-cli-proxy-image",
				Status:      StatusFailed,
				ReasonCode:  ErrUpstream,
			},
			{
				Order:       2,
				Provider:    TraceProviderAPIMart,
				ChannelID:   2,
				ChannelName: "apimart-image",
				Status:      StatusFailed,
				ReasonCode:  ErrUpstream,
			},
			{
				Order:           3,
				Provider:        TraceProviderFreeRunner,
				AccountID:       42,
				AccountPlanType: "free",
				Status:          StatusSuccess,
			},
		},
	}

	got := TaskTraceSummary(trace)
	if !strings.Contains(got, "Codex(codex-cli-proxy-image)") {
		t.Fatalf("summary should include original codex route, got %q", got)
	}
	if !strings.Contains(got, "APIMart(apimart-image)") {
		t.Fatalf("summary should include fallback apimart route, got %q", got)
	}
	if !strings.Contains(got, "Free Runner(#42/free)") {
		t.Fatalf("summary should include final free runner account, got %q", got)
	}
}

func TestTaskTraceDiagnosticsNormalizeErrorLayer(t *testing.T) {
	trace := &TaskTrace{}
	trace.SetRequestIDs("req-abc", "img_abc")
	trace.SetUpstreamRequestID(" upstream-1 ")
	trace.SetErrorLayer(ErrorLayerPolling)

	decoded := DecodeProviderTrace(EncodeProviderTrace(trace))
	if decoded == nil {
		t.Fatal("decoded trace is nil")
	}
	if decoded.RequestID != "req-abc" || decoded.TaskID != "img_abc" {
		t.Fatalf("request/task ids = (%q,%q)", decoded.RequestID, decoded.TaskID)
	}
	if decoded.UpstreamRequestID != "upstream-1" {
		t.Fatalf("upstream request id = %q", decoded.UpstreamRequestID)
	}
	if decoded.ErrorLayer != ErrorLayerPolling || decoded.ErrorLayerLabel != "轮询" {
		t.Fatalf("error layer = (%q,%q)", decoded.ErrorLayer, decoded.ErrorLayerLabel)
	}
}

func TestInferErrorLayerFromTrace(t *testing.T) {
	tests := []struct {
		name  string
		trace *TaskTrace
		code  string
		want  string
	}{
		{name: "queued", trace: nil, code: ErrInterrupted, want: ErrorLayerTaskQueue},
		{name: "poll timeout", trace: &TaskTrace{}, code: ErrPollTimeout, want: ErrorLayerPolling},
		{name: "apimart", trace: &TaskTrace{Final: TaskTraceEndpoint{Provider: TraceProviderAPIMart}}, code: ErrUpstream, want: ErrorLayerDownstreamAPIMart},
		{name: "fallback", trace: &TaskTrace{Final: TaskTraceEndpoint{Provider: TraceProviderFreeRunner}}, code: ErrUpstream, want: ErrorLayerGatewayFallback},
		{name: "downstream", trace: &TaskTrace{Final: TaskTraceEndpoint{Provider: TraceProviderCodex}}, code: ErrUpstream, want: ErrorLayerDownstreamBackend},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InferErrorLayer(tt.trace, tt.code); got != tt.want {
				t.Fatalf("InferErrorLayer() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTaskDecodeProviderTraceHandlesStoredJSON(t *testing.T) {
	raw, err := json.Marshal(&TaskTrace{
		Final: TaskTraceEndpoint{
			Provider:        TraceProviderFreeRunner,
			AccountID:       7,
			AccountPlanType: "free",
			Status:          StatusSuccess,
		},
	})
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}

	task := &Task{ProviderTrace: raw}
	trace := task.DecodeProviderTrace()
	if trace == nil {
		t.Fatal("DecodeProviderTrace() returned nil")
	}
	if trace.Final.Provider != TraceProviderFreeRunner || trace.Final.AccountID != 7 {
		t.Fatalf("decoded trace = %#v", trace.Final)
	}
}
