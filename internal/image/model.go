// Package image 异步生图任务的数据模型、DAO 以及同步 Runner。
//
// M3 首版采用「同步直出 + 异步查询」混合路线:
//
//   - /v1/images/generations 默认是同步(wait_for_result=true),请求直接
//     阻塞到图片生成完成再返回。由网关层的 goroutine pool 承接并发(目标
//     1000 并发),每个任务落库 + 走一次完整的上游协议链路。
//   - /v1/images/tasks/:id 可作为异步查询入口,客户端也可设
//     wait_for_result=false 拿到 task_id 后自行轮询(适合移动端/脚本)。
//
// 这样能复用现有的 Account + Proxy + 计费 + 限流路径,不引入额外的 Redis
// Stream 基础设施;等并发压力上来后再把 Runner 接到 Stream 消费者即可。
package image

import (
	"strings"
	"time"
)

// 任务状态。
const (
	StatusQueued     = "queued"     // 已入库,等调度
	StatusDispatched = "dispatched" // 已拿到 lease,未开始跑上游
	StatusRunning    = "running"    // 上游 SSE 已发起
	StatusSuccess    = "success"
	StatusFailed     = "failed"
)

// 错误码(短字符串,便于排查 & 计费对账)。
const (
	ErrUnknown           = "unknown"
	ErrNoAccount         = "no_available_account"
	ErrAuthRequired      = "auth_required"
	ErrAccountForbidden  = "account_forbidden"
	ErrRateLimited       = "rate_limited"
	ErrNetworkTransient  = "network_transient" // 瞬态网络错误(EOF / reset),可自动重试
	ErrPOWTimeout        = "pow_timeout"
	ErrPOWFailed         = "pow_failed"
	ErrTurnstile         = "turnstile_required"
	ErrUpstream          = "upstream_error"
	ErrPollTimeout       = "poll_timeout"
	ErrInterrupted       = "interrupted"
	ErrDownload          = "download_failed"
	ErrInvalidResponse   = "invalid_response"
	ErrContentModeration = "content_moderation"
	ErrChannelResolve    = "channel_resolve_failed"
	ErrChannelConnect    = "channel_connect_failed"
	ErrReferenceTimeout  = "reference_image_timeout"
	ErrReferenceTooLarge = "reference_image_too_large"
	ErrUpstream4xx       = "upstream_4xx"
	ErrUpstream5xx       = "upstream_5xx"
)

// FormatTaskError 把稳定错误码和原始错误详情合并进 image_tasks.error。
// 该列历史上只存短错误码；保留前缀码便于计费/统计，同时把详情留给任务查询排障。
func FormatTaskError(code, detail string) string {
	code = strings.TrimSpace(code)
	detail = strings.Join(strings.Fields(strings.TrimSpace(detail)), " ")
	if code == "" {
		code = ErrUnknown
	}
	if detail == "" || detail == code {
		return code
	}
	return code + ": " + detail
}

// SplitTaskError 拆分 FormatTaskError 的结果；也兼容历史上直接落库的上游原始错误。
func SplitTaskError(value string) (code, detail string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if idx := strings.Index(value, ":"); idx > 0 {
		candidate := strings.TrimSpace(value[:idx])
		if isTaskErrorCode(candidate) {
			return candidate, strings.TrimSpace(value[idx+1:])
		}
	}
	if strings.HasPrefix(strings.ToLower(value), "upstream ") {
		return ErrUpstream, value
	}
	return value, ""
}

// TaskErrorFields 把落库错误转换为对用户/下游都友好的稳定字段。
func TaskErrorFields(stored string) (code, detail, message string) {
	code, detail = SplitTaskError(stored)
	if code == "" {
		code = "task_failed"
	}
	message = LocalizeTaskError(code, detail)
	return code, detail, message
}

// LocalizeTaskError 把错误码和上游详情压成用户可直接理解的中文文案。
func LocalizeTaskError(code, raw string) string {
	raw = strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	var zh string
	switch code {
	case ErrNoAccount:
		zh = "账号池暂无可用账号,请稍后重试"
	case ErrRateLimited:
		zh = "上游风控,请稍后再试"
	case ErrAccountForbidden:
		zh = "上游临时拒绝当前账号,系统会切换账号或稍后复试"
	case ErrUnknown, "":
		zh = "图片生成失败"
	case ErrInterrupted:
		zh = "任务被服务重启中断,请重新提交"
	case ErrContentModeration:
		zh = "上游内容安全策略拒绝了本次生图,请调整 prompt 或参考图后重试"
	case ErrPollTimeout:
		zh = "图片生成超时,上游长时间没有返回图片"
	case ErrUpstream:
		zh = "上游返回错误"
	case ErrChannelResolve:
		zh = "图片渠道解析失败"
	case ErrChannelConnect:
		zh = "图片渠道连接失败"
	case ErrReferenceTimeout:
		zh = "参考图下载超时"
	case ErrReferenceTooLarge:
		zh = "参考图体积超过限制"
	case ErrUpstream4xx:
		zh = "上游拒绝了本次图片请求"
	case ErrUpstream5xx:
		zh = "上游服务异常"
	default:
		zh = "图片生成失败(" + code + ")"
	}
	if raw != "" && raw != code {
		if assistant := DiagnosticField(raw, "assistant"); assistant != "" {
			return zh + ":上游说明:" + assistant
		}
		return zh + ":" + raw
	}
	return zh
}

// DiagnosticField 从 `base; assistant: ...; last_error: ...` 这类诊断文本中取指定字段。
func DiagnosticField(raw, field string) string {
	raw = strings.TrimSpace(raw)
	field = strings.ToLower(strings.TrimSpace(field))
	if raw == "" || field == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	marker := field + ":"
	idx := strings.Index(lower, marker)
	if idx < 0 {
		return ""
	}
	start := idx + len(marker)
	text := raw[start:]
	lowerText := lower[start:]
	end := len(text)
	for _, next := range []string{"; assistant:", "; last_error:"} {
		if nextIdx := strings.Index(lowerText, next); nextIdx >= 0 && nextIdx < end {
			end = nextIdx
		}
	}
	return strings.TrimSpace(text[:end])
}

func isTaskErrorCode(value string) bool {
	if value == "" || len(value) > 80 || strings.ContainsAny(value, " \t\n\r") {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// Task 对应 image_tasks 表。
type Task struct {
	ID                  uint64     `db:"id"               json:"id"`
	TaskID              string     `db:"task_id"          json:"task_id"`
	UserID              uint64     `db:"user_id"          json:"user_id"`
	KeyID               uint64     `db:"key_id"           json:"key_id"`
	ModelID             uint64     `db:"model_id"         json:"model_id"`
	AccountID           uint64     `db:"account_id"       json:"account_id"`
	DownstreamUserID    string     `db:"downstream_user_id"    json:"downstream_user_id"`
	DownstreamUsername  string     `db:"downstream_username"   json:"downstream_username"`
	DownstreamUserEmail string     `db:"downstream_user_email" json:"downstream_user_email"`
	DownstreamUserLabel string     `db:"downstream_user_label" json:"downstream_user_label"`
	Prompt              string     `db:"prompt"           json:"prompt"`
	N                   int        `db:"n"                json:"n"`
	Size                string     `db:"size"             json:"size"`
	Upscale             string     `db:"upscale"          json:"upscale"`
	Status              string     `db:"status"           json:"status"`
	ConversationID      string     `db:"conversation_id"  json:"conversation_id"`
	FileIDs             []byte     `db:"file_ids"         json:"-"`
	ResultURLs          []byte     `db:"result_urls"      json:"-"`
	ProviderTrace       []byte     `db:"provider_trace"   json:"-"`
	Error               string     `db:"error"            json:"error"`
	EstimatedCredit     int64      `db:"estimated_credit" json:"estimated_credit"`
	CreditCost          int64      `db:"credit_cost"      json:"credit_cost"`
	CreatedAt           time.Time  `db:"created_at"       json:"created_at"`
	StartedAt           *time.Time `db:"started_at"       json:"started_at"`
	FinishedAt          *time.Time `db:"finished_at"      json:"finished_at"`
}

// Result 是 Runner 返回给网关/客户端的生图结果。
type Result struct {
	TaskID         string        `json:"task_id"`
	Status         string        `json:"status"`
	ConversationID string        `json:"conversation_id,omitempty"`
	Images         []ResultImage `json:"images,omitempty"`
	ErrorCode      string        `json:"error_code,omitempty"`
	ErrorMessage   string        `json:"error_message,omitempty"`
	CreditCost     int64         `json:"credit_cost"`
}

// ResultImage 单张生图。
type ResultImage struct {
	URL         string `json:"url"`     // 上游签名直链(短期有效,通常 15 分钟)
	FileID      string `json:"file_id"` // chatgpt.com file-service id(纯 id,不含 sed:)
	IsSediment  bool   `json:"is_sediment,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}
