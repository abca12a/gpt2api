package gateway

import (
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/internal/apikey"
	"github.com/432539/gpt2api/internal/config"
	imagepkg "github.com/432539/gpt2api/internal/image"
)

const (
	downstreamUserIDHeader    = "X-NewAPI-User-ID"
	downstreamUsernameHeader  = "X-NewAPI-Username"
	downstreamUserEmailHeader = "X-NewAPI-User-Email"
)

type downstreamUserInfo struct {
	ID       string
	Username string
	Email    string
	Label    string
}

func downstreamUserInfoForTask(c *gin.Context, ak *apikey.APIKey, requestUser string) downstreamUserInfo {
	if ak == nil || !isTrustedDownstreamKeyID(ak.ID, configuredTrustedDownstreamKeyIDs()) {
		return downstreamUserInfo{}
	}
	return downstreamUserInfoFromRequest(c.Request, requestUser, true)
}

func (info downstreamUserInfo) applyToTask(t *imagepkg.Task) {
	if t == nil {
		return
	}
	t.DownstreamUserID = info.ID
	t.DownstreamUsername = info.Username
	t.DownstreamUserEmail = info.Email
	t.DownstreamUserLabel = info.Label
}

func downstreamUserInfoFromRequest(req *http.Request, requestUser string, trusted bool) downstreamUserInfo {
	if !trusted {
		return downstreamUserInfo{}
	}
	info := downstreamUserInfo{}
	if req != nil {
		info.ID = parseDownstreamUserID(req.Header.Get(downstreamUserIDHeader))
		info.Username = cleanDownstreamValue(req.Header.Get(downstreamUsernameHeader), 128)
		info.Email = cleanDownstreamEmail(req.Header.Get(downstreamUserEmailHeader))
	}
	if info.ID == "" && info.Username == "" && info.Email == "" {
		info = parseDownstreamUserLabel(requestUser)
	}
	info.Label = buildDownstreamUserLabel(info, requestUser)
	return info
}

func isTrustedDownstreamKeyID(keyID uint64, trustedIDs []uint64) bool {
	if keyID == 0 || len(trustedIDs) == 0 {
		return false
	}
	for _, id := range trustedIDs {
		if id == keyID {
			return true
		}
	}
	return false
}

func configuredTrustedDownstreamKeyIDs() (ids []uint64) {
	defer func() {
		if recover() != nil {
			ids = nil
		}
	}()
	cfg := config.Get()
	if cfg == nil {
		return nil
	}
	return cfg.Security.TrustedDownstreamAPIKeyIDs
}

func parseDownstreamUserLabel(value string) downstreamUserInfo {
	value = cleanDownstreamValue(value, 255)
	if value == "" {
		return downstreamUserInfo{}
	}
	info := downstreamUserInfo{Label: value}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == '|' || r == ',' }) {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "id", "uid", "user_id", "newapi_user_id":
			info.ID = cleanDownstreamValue(val, 64)
		case "username", "name", "user_name":
			info.Username = cleanDownstreamValue(val, 128)
		case "email", "user_email":
			info.Email = cleanDownstreamEmail(val)
		}
	}
	return info
}

func buildDownstreamUserLabel(info downstreamUserInfo, fallback string) string {
	parts := make([]string, 0, 3)
	if info.ID != "" {
		parts = append(parts, "#"+info.ID)
	}
	if info.Username != "" {
		parts = append(parts, info.Username)
	}
	if info.Email != "" {
		parts = append(parts, info.Email)
	}
	if len(parts) > 0 {
		return cleanDownstreamValue(strings.Join(parts, " / "), 255)
	}
	return cleanDownstreamValue(fallback, 255)
}

func cleanDownstreamValue(value string, max int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if max > 0 && len(value) > max {
		return value[:max]
	}
	return value
}

func cleanDownstreamEmail(value string) string {
	value = strings.ToLower(cleanDownstreamValue(value, 128))
	if value == "" {
		return ""
	}
	addr, err := mail.ParseAddress(value)
	if err != nil || addr.Address == "" || strings.ContainsAny(addr.Address, " \t\n\r") {
		return ""
	}
	return cleanDownstreamValue(addr.Address, 128)
}

func canonicalDownstreamUserLabel(userID int, username, email string) string {
	parts := []string{fmt.Sprintf("user_id=%d", userID)}
	if username = cleanDownstreamValue(username, 128); username != "" {
		parts = append(parts, "username="+username)
	}
	if email = cleanDownstreamEmail(email); email != "" {
		parts = append(parts, "email="+email)
	}
	return strings.Join(parts, ";")
}

func parseDownstreamUserID(value string) string {
	value = cleanDownstreamValue(value, 64)
	if value == "" {
		return ""
	}
	if _, err := strconv.ParseUint(value, 10, 64); err != nil {
		return ""
	}
	return value
}
