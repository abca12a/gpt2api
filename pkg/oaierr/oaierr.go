package oaierr

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type PayloadBody struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Message string      `json:"message"`
	Type    string      `json:"type"`
	Code    interface{} `json:"code"`
}

func Abort(c *gin.Context, status int, code, message string) {
	if WantsAPIMart(c) {
		c.AbortWithStatusJSON(status, APIMartPayload(status, message))
		return
	}
	c.AbortWithStatusJSON(status, Payload(status, code, message))
}

func Payload(status int, code, message string) PayloadBody {
	if code == "" {
		code = TypeForStatus(status)
	}
	if message == "" {
		message = DefaultMessage(status)
	}
	return PayloadBody{Error: ErrorBody{
		Message: message,
		Type:    TypeForStatus(status),
		Code:    code,
	}}
}

func APIMartPayload(status int, message string) PayloadBody {
	if message == "" {
		message = DefaultMessage(status)
	}
	return PayloadBody{Error: ErrorBody{
		Message: message,
		Type:    TypeForStatus(status),
		Code:    status,
	}}
}

func TypeForStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "payment_required"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "service_unavailable"
	}
	if status >= http.StatusInternalServerError {
		return "server_error"
	}
	return "invalid_request_error"
}

func DefaultMessage(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "参数错误：size 不合法 / 4K 比例不支持 / 像素违规等"
	case http.StatusUnauthorized:
		return "身份验证失败，请检查您的API密钥"
	case http.StatusForbidden:
		return "权限不足，请检查您的API密钥或模型权限"
	case http.StatusNotFound:
		return "资源不存在"
	case http.StatusPaymentRequired:
		return "账户余额不足，请充值后再试"
	case http.StatusTooManyRequests:
		return "请求过于频繁，请稍后再试"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "上游暂时不可用，请稍后再试"
	case http.StatusInternalServerError:
		return "服务器错误"
	default:
		if status >= http.StatusInternalServerError {
			return "服务器错误"
		}
		return "请求失败"
	}
}

func WantsAPIMart(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	query := c.Request.URL.Query()
	for _, key := range []string{"compat", "response_schema", "schema", "format"} {
		if isAPIMartValue(query.Get(key)) {
			return true
		}
	}
	if isTruthyAPIMart(query.Get("apimart")) {
		return true
	}
	for _, key := range []string{"X-Response-Format", "X-API-Format", "X-Compat-Mode"} {
		if isAPIMartValue(c.Request.Header.Get(key)) {
			return true
		}
	}
	return false
}

func isAPIMartValue(value string) bool {
	normalized := normalizeCompatValue(value)
	return normalized == "apimart" || normalized == "apimartcompatible"
}

func isTruthyAPIMart(value string) bool {
	switch normalizeCompatValue(value) {
	case "apimart", "apimartcompatible", "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func normalizeCompatValue(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, "_", "")
	normalized = strings.ReplaceAll(normalized, " ", "")
	return normalized
}
