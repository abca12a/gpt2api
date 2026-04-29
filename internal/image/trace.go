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

type TaskTrace struct {
	Original TaskTraceEndpoint  `json:"original,omitempty"`
	Fallback *TaskTraceFallback `json:"fallback,omitempty"`
	Final    TaskTraceEndpoint  `json:"final,omitempty"`
	Steps    []TaskTraceStep    `json:"steps,omitempty"`
	Timing   *TaskTraceTiming   `json:"timing,omitempty"`
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
	Order           int    `json:"order"`
	Provider        string `json:"provider,omitempty"`
	ChannelID       uint64 `json:"channel_id,omitempty"`
	ChannelName     string `json:"channel_name,omitempty"`
	AccountID       uint64 `json:"account_id,omitempty"`
	AccountPlanType string `json:"account_plan_type,omitempty"`
	Status          string `json:"status,omitempty"`
	ReasonCode      string `json:"reason_code,omitempty"`
	ReasonDetail    string `json:"reason_detail,omitempty"`
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
	t.Steps = append(t.Steps, step)
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

func normalizeTaskTrace(trace *TaskTrace) *TaskTrace {
	if trace == nil {
		return nil
	}
	if trace.Original.Provider == "" && trace.Final.Provider == "" && len(trace.Steps) == 0 && trace.Fallback == nil && trace.Timing == nil {
		return nil
	}
	return trace
}
