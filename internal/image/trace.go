package image

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	TraceProviderCodex         = "codex"
	TraceProviderAPIMart       = "apimart"
	TraceProviderOpenAI        = "openai"
	TraceProviderGemini        = "gemini"
	TraceProviderAccountRunner = "account_runner"
	TraceProviderFreeRunner    = "free_runner"
	TraceProviderUnknown       = "unknown"
)

const (
	ErrorLayerGatewayEntry      = "gateway_entry"
	ErrorLayerTaskQueue         = "task_queue"
	ErrorLayerPolling           = "polling"
	ErrorLayerGatewayFallback   = "gateway_fallback"
	ErrorLayerDownstreamBackend = "downstream_backend"
	ErrorLayerDownstreamAPIMart = "downstream_apimart"
)

type TaskTrace struct {
	RequestID         string             `json:"request_id,omitempty"`
	TaskID            string             `json:"task_id,omitempty"`
	UpstreamRequestID string             `json:"upstream_request_id,omitempty"`
	DownstreamStatus  string             `json:"downstream_status,omitempty"`
	ErrorLayer        string             `json:"error_layer,omitempty"`
	ErrorLayerLabel   string             `json:"error_layer_label,omitempty"`
	Original          TaskTraceEndpoint  `json:"original,omitempty"`
	Fallback          *TaskTraceFallback `json:"fallback,omitempty"`
	Final             TaskTraceEndpoint  `json:"final,omitempty"`
	Steps             []TaskTraceStep    `json:"steps,omitempty"`
	Timing            *TaskTraceTiming   `json:"timing,omitempty"`
}

type TaskTraceEndpoint struct {
	Provider        string `json:"provider,omitempty"`
	ChannelID       uint64 `json:"channel_id,omitempty"`
	ChannelName     string `json:"channel_name,omitempty"`
	AccountID       uint64 `json:"account_id,omitempty"`
	AccountPlanType string `json:"account_plan_type,omitempty"`
	Status          string `json:"status,omitempty"`
}

type TaskTraceFallback struct {
	Triggered       bool   `json:"triggered"`
	ReasonCode      string `json:"reason_code,omitempty"`
	ReasonDetail    string `json:"reason_detail,omitempty"`
	FromProvider    string `json:"from_provider,omitempty"`
	FromChannelID   uint64 `json:"from_channel_id,omitempty"`
	FromChannelName string `json:"from_channel_name,omitempty"`
}

type TaskTraceStep struct {
	Order             int    `json:"order"`
	Provider          string `json:"provider,omitempty"`
	ChannelID         uint64 `json:"channel_id,omitempty"`
	ChannelName       string `json:"channel_name,omitempty"`
	UpstreamRequestID string `json:"upstream_request_id,omitempty"`
	DownstreamStatus  string `json:"downstream_status,omitempty"`
	ErrorLayer        string `json:"error_layer,omitempty"`
	ErrorLayerLabel   string `json:"error_layer_label,omitempty"`
	AccountID         uint64 `json:"account_id,omitempty"`
	AccountPlanType   string `json:"account_plan_type,omitempty"`
	Status            string `json:"status,omitempty"`
	ReasonCode        string `json:"reason_code,omitempty"`
	ReasonDetail      string `json:"reason_detail,omitempty"`
}

func EncodeProviderTrace(trace *TaskTrace) []byte {
	trace = normalizeTaskTrace(trace)
	if trace == nil {
		return nil
	}
	data, err := json.Marshal(trace)
	if err != nil {
		return nil
	}
	return data
}

func DecodeProviderTrace(raw []byte) *TaskTrace {
	if len(raw) == 0 {
		return nil
	}
	var trace TaskTrace
	if err := json.Unmarshal(raw, &trace); err != nil {
		return nil
	}
	return normalizeTaskTrace(&trace)
}

func (t *Task) DecodeProviderTrace() *TaskTrace {
	if t == nil {
		return nil
	}
	return DecodeProviderTrace(t.ProviderTrace)
}

func (t *TaskTrace) AddStep(step TaskTraceStep) {
	if t == nil {
		return
	}
	step.Provider = strings.TrimSpace(step.Provider)
	step.ChannelName = strings.TrimSpace(step.ChannelName)
	step.UpstreamRequestID = strings.TrimSpace(step.UpstreamRequestID)
	step.DownstreamStatus = strings.TrimSpace(step.DownstreamStatus)
	step.ErrorLayer = normalizeErrorLayer(step.ErrorLayer)
	step.ErrorLayerLabel = ErrorLayerLabel(step.ErrorLayer)
	step.AccountPlanType = strings.TrimSpace(step.AccountPlanType)
	step.ReasonCode = strings.TrimSpace(step.ReasonCode)
	step.ReasonDetail = strings.Join(strings.Fields(strings.TrimSpace(step.ReasonDetail)), " ")
	if step.Order <= 0 {
		step.Order = len(t.Steps) + 1
	}
	if t.Original.Provider == "" {
		t.Original = step.endpoint()
	}
	endpoint := step.endpoint()
	endpoint.Status = step.Status
	t.Final = endpoint
	if step.UpstreamRequestID != "" {
		t.SetUpstreamRequestID(step.UpstreamRequestID)
	}
	if step.DownstreamStatus != "" {
		t.SetDownstreamStatus(step.DownstreamStatus)
	}
	if step.Status == StatusFailed && t.ErrorLayer == "" {
		t.SetErrorLayer(step.ErrorLayer)
	}
	t.Steps = append(t.Steps, step)
}

func (t *TaskTrace) SetRequestIDs(requestID, taskID string) {
	if t == nil {
		return
	}
	t.RequestID = strings.TrimSpace(requestID)
	t.TaskID = strings.TrimSpace(taskID)
}

func (t *TaskTrace) SetUpstreamRequestID(upstreamRequestID string) {
	if t == nil {
		return
	}
	upstreamRequestID = strings.TrimSpace(upstreamRequestID)
	if upstreamRequestID != "" {
		t.UpstreamRequestID = upstreamRequestID
	}
}

func (t *TaskTrace) SetDownstreamStatus(status string) {
	if t == nil {
		return
	}
	status = strings.TrimSpace(status)
	if status != "" {
		t.DownstreamStatus = status
	}
}

func (t *TaskTrace) SetErrorLayer(layer string) {
	if t == nil {
		return
	}
	layer = normalizeErrorLayer(layer)
	if layer == "" {
		return
	}
	t.ErrorLayer = layer
	t.ErrorLayerLabel = ErrorLayerLabel(layer)
}

func (t *TaskTrace) MarkFallback(step TaskTraceStep, reasonCode, reasonDetail string) {
	if t == nil {
		return
	}
	t.Fallback = &TaskTraceFallback{
		Triggered:       true,
		ReasonCode:      strings.TrimSpace(reasonCode),
		ReasonDetail:    strings.Join(strings.Fields(strings.TrimSpace(reasonDetail)), " "),
		FromProvider:    strings.TrimSpace(step.Provider),
		FromChannelID:   step.ChannelID,
		FromChannelName: strings.TrimSpace(step.ChannelName),
	}
}

func (s TaskTraceStep) endpoint() TaskTraceEndpoint {
	return TaskTraceEndpoint{
		Provider:        s.Provider,
		ChannelID:       s.ChannelID,
		ChannelName:     s.ChannelName,
		AccountID:       s.AccountID,
		AccountPlanType: s.AccountPlanType,
	}
}

func TaskTraceSummary(trace *TaskTrace) string {
	trace = normalizeTaskTrace(trace)
	if trace == nil {
		return ""
	}
	parts := make([]string, 0, len(trace.Steps))
	for _, step := range trace.Steps {
		label := taskTraceStepLabel(step)
		if label == "" {
			continue
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		if label := taskTraceEndpointLabel(trace.Final); label != "" {
			return label
		}
		return taskTraceEndpointLabel(trace.Original)
	}
	return strings.Join(parts, " -> ")
}

func taskTraceStepLabel(step TaskTraceStep) string {
	base := taskTraceEndpointLabel(step.endpoint())
	if base == "" {
		return ""
	}
	if strings.TrimSpace(step.ReasonCode) == "" || step.Status == StatusSuccess {
		return base
	}
	return fmt.Sprintf("%s[%s]", base, step.ReasonCode)
}

func taskTraceEndpointLabel(endpoint TaskTraceEndpoint) string {
	name := traceProviderDisplayName(endpoint.Provider)
	switch endpoint.Provider {
	case TraceProviderFreeRunner:
		if endpoint.AccountID > 0 && endpoint.AccountPlanType != "" {
			return fmt.Sprintf("%s(#%d/%s)", name, endpoint.AccountID, endpoint.AccountPlanType)
		}
		if endpoint.AccountID > 0 {
			return fmt.Sprintf("%s(#%d)", name, endpoint.AccountID)
		}
	case TraceProviderAccountRunner:
		if endpoint.AccountID > 0 && endpoint.AccountPlanType != "" {
			return fmt.Sprintf("%s(#%d/%s)", name, endpoint.AccountID, endpoint.AccountPlanType)
		}
		if endpoint.AccountID > 0 {
			return fmt.Sprintf("%s(#%d)", name, endpoint.AccountID)
		}
	}
	if endpoint.ChannelName != "" {
		return fmt.Sprintf("%s(%s)", name, endpoint.ChannelName)
	}
	return name
}

func traceProviderDisplayName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case TraceProviderCodex:
		return "Codex"
	case TraceProviderAPIMart:
		return "APIMart"
	case TraceProviderOpenAI:
		return "OpenAI"
	case TraceProviderGemini:
		return "Gemini"
	case TraceProviderAccountRunner:
		return "内置账号池"
	case TraceProviderFreeRunner:
		return "Free Runner"
	default:
		return "未知渠道"
	}
}

func ErrorLayerLabel(layer string) string {
	switch normalizeErrorLayer(layer) {
	case ErrorLayerGatewayEntry:
		return "号池入口"
	case ErrorLayerTaskQueue:
		return "任务队列"
	case ErrorLayerPolling:
		return "轮询"
	case ErrorLayerGatewayFallback:
		return "号池兜底"
	case ErrorLayerDownstreamBackend:
		return "下游后端"
	case ErrorLayerDownstreamAPIMart:
		return "下游 apimart"
	default:
		return ""
	}
}

func InferErrorLayer(trace *TaskTrace, errorCode string) string {
	trace = normalizeTaskTrace(trace)
	if trace != nil && trace.ErrorLayer != "" {
		return trace.ErrorLayer
	}
	switch strings.TrimSpace(errorCode) {
	case ErrInterrupted, ErrNoAccount:
		return ErrorLayerTaskQueue
	case ErrPollTimeout:
		return ErrorLayerPolling
	}
	if trace == nil {
		return ErrorLayerGatewayEntry
	}
	provider := trace.Final.Provider
	if provider == "" && len(trace.Steps) > 0 {
		provider = trace.Steps[len(trace.Steps)-1].Provider
	}
	switch normalizeProviderKey(provider) {
	case TraceProviderAPIMart:
		return ErrorLayerDownstreamAPIMart
	case TraceProviderFreeRunner, TraceProviderAccountRunner:
		return ErrorLayerGatewayFallback
	case TraceProviderCodex, TraceProviderOpenAI, TraceProviderGemini:
		return ErrorLayerDownstreamBackend
	default:
		return ErrorLayerGatewayEntry
	}
}

func normalizeErrorLayer(layer string) string {
	switch strings.ToLower(strings.TrimSpace(layer)) {
	case ErrorLayerGatewayEntry, ErrorLayerTaskQueue, ErrorLayerPolling, ErrorLayerGatewayFallback, ErrorLayerDownstreamBackend, ErrorLayerDownstreamAPIMart:
		return strings.ToLower(strings.TrimSpace(layer))
	default:
		return ""
	}
}

func normalizeTaskTrace(trace *TaskTrace) *TaskTrace {
	if trace == nil {
		return nil
	}
	trace.RequestID = strings.TrimSpace(trace.RequestID)
	trace.TaskID = strings.TrimSpace(trace.TaskID)
	trace.UpstreamRequestID = strings.TrimSpace(trace.UpstreamRequestID)
	trace.DownstreamStatus = strings.TrimSpace(trace.DownstreamStatus)
	trace.ErrorLayer = normalizeErrorLayer(trace.ErrorLayer)
	trace.ErrorLayerLabel = ErrorLayerLabel(trace.ErrorLayer)
	if trace.Original.Provider == "" && trace.Final.Provider == "" && len(trace.Steps) == 0 && trace.Fallback == nil && trace.Timing == nil && trace.RequestID == "" && trace.TaskID == "" && trace.UpstreamRequestID == "" && trace.DownstreamStatus == "" && trace.ErrorLayer == "" {
		return nil
	}
	return trace
}
