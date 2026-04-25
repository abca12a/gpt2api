package apikey

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/pkg/oaierr"
)

const (
	CtxKey      = "apikey"
	CtxKeyOwner = "apikey_user_id"
)

// Middleware 返回一个 gin 中间件,按 OpenAI 规范校验 Bearer Key。
// allowQuery=true 允许 ?api_key= 作为兜底(浏览器直出)。
func Middleware(svc *Service, allowQuery bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := extractKey(c, allowQuery)
		if raw == "" {
			openAIAuthError(c, "missing_api_key", "缺少 API Key,请在 Authorization 头中传入 Bearer <key>")
			return
		}
		k, err := svc.Verify(c.Request.Context(), raw)
		if err != nil {
			openAIAuthError(c, "invalid_api_key", err.Error())
			return
		}
		ip := c.ClientIP()
		if !k.IPAllowed(ip) {
			openAIAuthError(c, "ip_not_allowed", "当前 IP 不在该 API Key 的白名单内")
			return
		}
		c.Set(CtxKey, k)
		c.Set(CtxKeyOwner, k.UserID)
		c.Next()
	}
}

func extractKey(c *gin.Context, allowQuery bool) string {
	h := c.GetHeader("Authorization")
	if h != "" {
		if strings.HasPrefix(h, "Bearer ") {
			return strings.TrimSpace(h[len("Bearer "):])
		}
		return strings.TrimSpace(h)
	}
	if allowQuery {
		if v := c.Query("api_key"); v != "" {
			return v
		}
	}
	return ""
}

// FromCtx 取回 APIKey 对象。
func FromCtx(c *gin.Context) (*APIKey, bool) {
	v, ok := c.Get(CtxKey)
	if !ok {
		return nil, false
	}
	k, ok := v.(*APIKey)
	return k, ok
}

// openAIAuthError 按 OpenAI 规范返回 401 错误,并支持 APIMart 兼容错误类型。
func openAIAuthError(c *gin.Context, code, msg string) {
	oaierr.Abort(c, 401, code, msg)
}
