package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// UpstreamHTTPError 保留 OpenAI 兼容渠道返回的结构化错误信息。
type UpstreamHTTPError struct {
	Status  int
	Code    string
	Type    string
	Message string
	Body    string
}

func (e *UpstreamHTTPError) Error() string {
	if e == nil {
		return "upstream error"
	}
	parts := make([]string, 0, 3)
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if e.Type != "" && e.Type != e.Code {
		parts = append(parts, e.Type)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if len(parts) > 0 {
		return fmt.Sprintf("upstream %d: %s", e.Status, strings.Join(parts, ": "))
	}
	return fmt.Sprintf("upstream %d: %s", e.Status, strings.TrimSpace(e.Body))
}

func newUpstreamHTTPError(resp *http.Response, body []byte) error {
	err := &UpstreamHTTPError{
		Status: resp.StatusCode,
		Body:   strings.TrimSpace(string(body)),
	}
	var payload struct {
		Error struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Code    any    `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	if json.Unmarshal(body, &payload) == nil {
		err.Code = valueString(payload.Error.Code)
		err.Type = payload.Error.Type
		err.Message = payload.Error.Message
		if err.Code == "" {
			err.Code = valueString(payload.Code)
		}
		if err.Type == "" {
			err.Type = payload.Type
		}
		if err.Message == "" {
			err.Message = payload.Message
		}
	}
	return err
}

func valueString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case float64:
		return fmt.Sprintf("%.0f", x)
	case int:
		return fmt.Sprint(x)
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

// IsContentModerationError 判断错误是否明确来自上游内容安全/审核策略。
func IsContentModerationError(err error) bool {
	if err == nil {
		return false
	}
	var upstream *UpstreamHTTPError
	if errors.As(err, &upstream) {
		return hasContentModerationSignal(upstream.Code, upstream.Type, upstream.Message, upstream.Body)
	}
	return hasContentModerationSignal(err.Error())
}

func hasContentModerationSignal(values ...string) bool {
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		compact := strings.ReplaceAll(normalized, "-", "_")
		compact = strings.ReplaceAll(compact, " ", "_")
		signals := []string{
			"content_policy_violation",
			"content_moderation",
			"moderation_blocked",
			"policy_violation",
			"responsibleaipolicyviolation",
			"content_policy",
			"safety_system",
			"safety_policy",
			"blocked_by_policy",
			"rejected_as_a_result_of_our_safety",
			"unsafe_content",
			"内容安全",
			"安全策略",
			"安全审核",
			"审核未通过",
			"内容审核",
			"内容违规",
			"违反内容",
			"被安全系统拒绝",
		}
		for _, signal := range signals {
			if strings.Contains(compact, signal) || strings.Contains(normalized, signal) {
				return true
			}
		}
	}
	return false
}
