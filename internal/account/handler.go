package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/internal/settings"
	"github.com/432539/gpt2api/pkg/resp"
)

// ProxyURLResolver 按 proxy_id 取代理 URL(已带密码),供 ImportTokens 时走 RT/ST 换 AT 使用。
// 由外部传入一个实现(通常是 proxy.Service 的包装),避免 account 包直接依赖 proxy 包。
type ProxyURLResolver interface {
	ProxyURLByID(ctx context.Context, proxyID uint64) string
}

type Handler struct {
	svc           *Service
	refresher     *Refresher
	prober        *QuotaProber
	settings      *settings.Service
	proxyResolver ProxyURLResolver
	oauth         *openAIOAuthManager
	appBaseURL    string
}

func NewHandler(s *Service) *Handler {
	return &Handler{
		svc:   s,
		oauth: newOpenAIOAuthManager(),
	}
}

// SetRefresher 注入刷新器(可选,未注入时相关接口返回 501)。
func (h *Handler) SetRefresher(r *Refresher) { h.refresher = r }

// SetProber 注入额度探测器(可选)。
func (h *Handler) SetProber(p *QuotaProber) { h.prober = p }

// SetSettings 注入系统设置服务,用于自动刷新开关的读写。
func (h *Handler) SetSettings(s *settings.Service) { h.settings = s }

// SetProxyResolver 注入代理 URL 解析器(可选,未注入时 RT/ST 批量导入只能直连)。
func (h *Handler) SetProxyResolver(r ProxyURLResolver) { h.proxyResolver = r }

// SetAppBaseURL 注入站点对外 base URL,用于 OAuth 默认回调地址。
func (h *Handler) SetAppBaseURL(baseURL string) {
	h.appBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

// POST /api/admin/accounts/oauth/generate-auth-url
func (h *Handler) GenerateOAuthURL(c *gin.Context) {
	if h.oauth == nil {
		resp.Internal(c, "oauth 未初始化")
		return
	}
	var req struct {
		ProxyID     uint64 `json:"proxy_id"`
		RedirectURI string `json:"redirect_uri"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !strings.Contains(err.Error(), "EOF") {
		resp.BadRequest(c, "请求参数错误:"+err.Error())
		return
	}

	var proxyURL string
	if req.ProxyID > 0 {
		if h.proxyResolver == nil {
			resp.BadRequest(c, "代理解析器未初始化")
			return
		}
		proxyURL = h.proxyResolver.ProxyURLByID(c.Request.Context(), req.ProxyID)
		if proxyURL == "" {
			resp.BadRequest(c, "代理不存在、未启用或无法构造 URL")
			return
		}
	}

	redirectURI := strings.TrimSpace(req.RedirectURI)
	if redirectURI == "" && h.appBaseURL != "" {
		redirectURI = h.appBaseURL + "/oauth/openai/callback"
	}

	out, err := h.oauth.GenerateAuthURL(redirectURI, proxyURL)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, out)
}

// POST /api/admin/accounts/oauth/exchange-code
func (h *Handler) ExchangeOAuthCode(c *gin.Context) {
	if h.oauth == nil {
		resp.Internal(c, "oauth 未初始化")
		return
	}
	var req struct {
		SessionID      string `json:"session_id"`
		CallbackURL    string `json:"callback_url"`
		Code           string `json:"code"`
		State          string `json:"state"`
		RedirectURI    string `json:"redirect_uri"`
		ProxyID        uint64 `json:"proxy_id"`
		AccountType    string `json:"account_type"`
		UpdateExisting *bool  `json:"update_existing"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, "请求参数错误:"+err.Error())
		return
	}
	if req.CallbackURL != "" && (strings.TrimSpace(req.Code) == "" || strings.TrimSpace(req.State) == "") {
		code, state, err := parseOAuthCallback(req.CallbackURL)
		if err != nil {
			resp.BadRequest(c, err.Error())
			return
		}
		if strings.TrimSpace(req.Code) == "" {
			req.Code = code
		}
		if strings.TrimSpace(req.State) == "" {
			req.State = state
		}
	}
	if strings.TrimSpace(req.SessionID) == "" {
		resp.BadRequest(c, "session_id 不能为空")
		return
	}
	if strings.TrimSpace(req.Code) == "" {
		resp.BadRequest(c, "code 不能为空")
		return
	}
	if strings.TrimSpace(req.State) == "" {
		resp.BadRequest(c, "state 不能为空")
		return
	}

	var proxyURL string
	if req.ProxyID > 0 {
		if h.proxyResolver == nil {
			resp.BadRequest(c, "代理解析器未初始化")
			return
		}
		proxyURL = h.proxyResolver.ProxyURLByID(c.Request.Context(), req.ProxyID)
		if proxyURL == "" {
			resp.BadRequest(c, "代理不存在、未启用或无法构造 URL")
			return
		}
	}

	out, err := h.oauth.ExchangeCode(c.Request.Context(), &openAIOAuthExchangeInput{
		SessionID:   strings.TrimSpace(req.SessionID),
		Code:        strings.TrimSpace(req.Code),
		State:       strings.TrimSpace(req.State),
		RedirectURI: strings.TrimSpace(req.RedirectURI),
		ProxyURL:    proxyURL,
		AccountType: strings.TrimSpace(req.AccountType),
	})
	if err != nil {
		resp.BadRequest(c, err.Error())
		return
	}

	updateExisting := true
	if req.UpdateExisting != nil {
		updateExisting = *req.UpdateExisting
	}
	summary := h.svc.ImportBatch(c.Request.Context(), []ImportSource{out.Source}, ImportOptions{
		UpdateExisting:  updateExisting,
		DefaultClientID: out.Source.ClientID,
		DefaultProxyID:  req.ProxyID,
		BatchSize:       1,
	})

	var result *ImportLineResult
	var accountItem *Account
	if len(summary.Results) > 0 {
		result = &summary.Results[0]
		if result.ID > 0 {
			current, _ := h.svc.Get(c.Request.Context(), result.ID)
			if out.Token != nil && out.Token.PlanType != "" && current != nil &&
				(result.Status == "created" || result.Status == "updated") {
				if a, err := h.svc.Update(c.Request.Context(), result.ID, UpdateInput{
					PlanType: out.Token.PlanType,
					Notes:    current.Notes,
				}); err == nil {
					accountItem = a
				}
			}
			if accountItem == nil {
				accountItem = current
			}
		}
	}

	if h.refresher != nil {
		h.refresher.Kick()
	}
	if h.prober != nil {
		h.prober.Kick()
	}

	resp.OK(c, gin.H{
		"summary": summary,
		"result":  result,
		"account": accountItem,
		"oauth": gin.H{
			"email":               out.Token.Email,
			"plan_type":           out.Token.PlanType,
			"chatgpt_account_id":  out.Token.ChatGPTAccountID,
			"client_id":           out.Token.ClientID,
			"expires_at":          out.Token.ExpiresAt,
			"redirect_uri_notice": openAIOAuthDefaultRedirectURI,
		},
	})
}

// POST /api/admin/accounts
func (h *Handler) Create(c *gin.Context) {
	var req CreateInput
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, "请求参数错误:"+err.Error())
		return
	}
	a, err := h.svc.Create(c.Request.Context(), req)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, a)
}

// GET /api/admin/accounts
func (h *Handler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	if size < 1 {
		size = 10
	}
	if size > 1000 {
		size = 1000
	}
	status := c.Query("status")
	keyword := c.Query("keyword")
	list, total, err := h.svc.List(c.Request.Context(), status, keyword, (page-1)*size, size)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"list": list, "total": total, "page": page, "page_size": size})
}

// GET /api/admin/accounts/:id
func (h *Handler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	a, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		resp.NotFound(c, err.Error())
		return
	}
	resp.OK(c, a)
}

// PATCH /api/admin/accounts/:id
func (h *Handler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req UpdateInput
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, "请求参数错误:"+err.Error())
		return
	}
	a, err := h.svc.Update(c.Request.Context(), id, req)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, a)
}

// DELETE /api/admin/accounts/:id
func (h *Handler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"deleted": id})
}

// GET /api/admin/accounts/:id/secrets
// 仅管理员可用,返回 AT / RT / ST 明文用于编辑弹窗回显。
func (h *Handler) GetSecrets(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	sec, err := h.svc.GetSecrets(c.Request.Context(), id)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, sec)
}

// POST /api/admin/accounts/bulk-delete
// body: { "scope": "dead" | "suspicious" | "warned" | "throttled" | "all" }
// 批量软删指定状态的账号;scope=all 时删除全部(调用方需二次确认)。
func (h *Handler) BulkDelete(c *gin.Context) {
	var req struct {
		Scope string `json:"scope" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, "请求参数错误:"+err.Error())
		return
	}
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	allowed := map[string]bool{
		"dead": true, "suspicious": true, "warned": true, "throttled": true, "all": true,
	}
	if !allowed[scope] {
		resp.BadRequest(c, "scope 仅支持 dead / suspicious / warned / throttled / all")
		return
	}
	n, err := h.svc.BulkDeleteByStatus(c.Request.Context(), scope)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"deleted": n, "scope": scope})
}

// ===================== 自动刷新开关 =====================

// GET /api/admin/accounts/auto-refresh
// 返回当前自动刷新配置。
func (h *Handler) GetAutoRefresh(c *gin.Context) {
	if h.settings == nil {
		resp.Internal(c, "系统设置未初始化")
		return
	}
	resp.OK(c, gin.H{
		"enabled":   h.settings.AccountRefreshEnabled(),
		"ahead_sec": h.settings.AccountRefreshAheadSec(),
		"threshold": "AT 距离过期 < 1 天时自动刷新,失效/可疑账号不刷新",
	})
}

// PUT /api/admin/accounts/auto-refresh
// body: { "enabled": true|false }
// 写入 account.refresh_enabled;同时把阈值固定为 86400(1 天)以满足 UI 语义。
func (h *Handler) SetAutoRefresh(c *gin.Context) {
	if h.settings == nil {
		resp.Internal(c, "系统设置未初始化")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, "请求参数错误:"+err.Error())
		return
	}
	updates := map[string]string{
		settings.AccountRefreshEnabled:  boolStr(req.Enabled),
		settings.AccountRefreshAheadSec: "86400",
	}
	if err := h.settings.Set(c.Request.Context(), updates); err != nil {
		resp.Internal(c, "保存失败:"+err.Error())
		return
	}
	if req.Enabled && h.refresher != nil {
		h.refresher.Kick() // 立刻扫一遍
	}
	resp.OK(c, gin.H{
		"enabled":   req.Enabled,
		"ahead_sec": 86400,
	})
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// 保留以便未来直接传 context(当前未用,但留一个显式符号避免删字段)
var _ = context.Background

// POST /api/admin/accounts/:id/bind-proxy
func (h *Handler) BindProxy(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		ProxyID uint64 `json:"proxy_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, "请求参数错误:"+err.Error())
		return
	}
	if err := h.svc.BindProxy(c.Request.Context(), id, req.ProxyID); err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"account_id": id, "proxy_id": req.ProxyID})
}

// DELETE /api/admin/accounts/:id/bind-proxy
func (h *Handler) UnbindProxy(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.svc.UnbindProxy(c.Request.Context(), id); err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"account_id": id})
}

// ===================== 批量导入 =====================

// POST /api/admin/accounts/import
// body: { text: "...", update_existing: true, default_client_id: "", default_proxy_id: 0 }
// 或 multipart/form-data:files[] + 其他字段
func (h *Handler) Import(c *gin.Context) {
	var req struct {
		Text            string `json:"text"`
		UpdateExisting  *bool  `json:"update_existing"`
		DefaultClientID string `json:"default_client_id"`
		DefaultProxyID  uint64 `json:"default_proxy_id"`
	}
	// 支持 JSON body 或 multipart
	ct := c.ContentType()
	if ct == "application/json" {
		if err := c.ShouldBindJSON(&req); err != nil {
			resp.BadRequest(c, "请求参数错误:"+err.Error())
			return
		}
	} else {
		// multipart 表单
		req.Text = c.PostForm("text")
		if v := c.PostForm("update_existing"); v != "" {
			b := v == "true" || v == "1"
			req.UpdateExisting = &b
		}
		req.DefaultClientID = c.PostForm("default_client_id")
		if v := c.PostForm("default_proxy_id"); v != "" {
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				req.DefaultProxyID = n
			}
		}
		// 多文件合并:允许前端一次上传 N 个 json
		if form, err := c.MultipartForm(); err == nil && form != nil {
			var sb strings.Builder
			if req.Text != "" {
				sb.WriteString(req.Text)
				sb.WriteByte('\n')
			}
			for _, fh := range form.File["files"] {
				f, err := fh.Open()
				if err != nil {
					continue
				}
				data, err := io.ReadAll(f)
				_ = f.Close()
				if err != nil || len(data) == 0 {
					continue
				}
				sb.Write(data)
				sb.WriteByte('\n')
			}
			req.Text = sb.String()
		}
	}

	if req.Text == "" {
		resp.BadRequest(c, "请提供 text 或上传文件")
		return
	}

	items, err := ParseJSONBlob(req.Text)
	if err != nil {
		resp.BadRequest(c, "解析失败:"+err.Error())
		return
	}

	upd := true
	if req.UpdateExisting != nil {
		upd = *req.UpdateExisting
	}

	opt := ImportOptions{
		UpdateExisting:  upd,
		DefaultClientID: req.DefaultClientID,
		DefaultProxyID:  req.DefaultProxyID,
		BatchSize:       200,
	}
	summary := h.svc.ImportBatch(c.Request.Context(), items, opt)

	// 后台踢一次刷新(让新导入的账号尽快探测过期时间 / 额度)
	if h.refresher != nil {
		h.refresher.Kick()
	}
	if h.prober != nil {
		h.prober.Kick()
	}

	resp.OK(c, summary)
}

// POST /api/admin/accounts/import-tokens
//
// body:
//
//	{
//	  "mode": "at" | "rt" | "st",
//	  "tokens": "一行一个\n...\n",   // 或字符串数组
//	  "client_id": "app_xxxx",      // rt 必填,at/st 可选
//	  "update_existing": true,
//	  "default_proxy_id": 0         // RT/ST 换 AT 时走此代理,强烈推荐
//	}
//
// 返回同 /import:ImportSummary。
func (h *Handler) ImportTokens(c *gin.Context) {
	var req struct {
		Mode           string          `json:"mode"`
		Tokens         json.RawMessage `json:"tokens"`
		ClientID       string          `json:"client_id"`
		UpdateExisting *bool           `json:"update_existing"`
		DefaultProxyID uint64          `json:"default_proxy_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, "请求参数错误:"+err.Error())
		return
	}

	// 支持两种 tokens 形态:字符串数组 / 一大段多行文本
	var tokens []string
	if len(req.Tokens) > 0 {
		switch bytes.TrimSpace(req.Tokens)[0] {
		case '[':
			_ = json.Unmarshal(req.Tokens, &tokens)
		case '"':
			var s string
			_ = json.Unmarshal(req.Tokens, &s)
			tokens = splitLines(s)
		}
	}
	if len(tokens) == 0 {
		resp.BadRequest(c, "tokens 不能为空,请每行一个")
		return
	}

	mode := ImportTokenMode(strings.ToLower(strings.TrimSpace(req.Mode)))
	if mode == "" {
		mode = ImportModeAT
	}
	if mode != ImportModeAT && mode != ImportModeRT && mode != ImportModeST {
		resp.BadRequest(c, "不支持的 mode(仅 at / rt / st)")
		return
	}
	if mode == ImportModeRT && strings.TrimSpace(req.ClientID) == "" {
		resp.BadRequest(c, "RT 模式必须提供 client_id(APPID)")
		return
	}

	upd := true
	if req.UpdateExisting != nil {
		upd = *req.UpdateExisting
	}

	var proxyURL string
	if req.DefaultProxyID > 0 && h.proxyResolver != nil {
		proxyURL = h.proxyResolver.ProxyURLByID(c.Request.Context(), req.DefaultProxyID)
	}

	summary := h.svc.ImportTokensBatch(c.Request.Context(), tokens, ImportTokensOptions{
		Mode:            mode,
		ClientID:        strings.TrimSpace(req.ClientID),
		ProxyURL:        proxyURL,
		DefaultProxyID:  req.DefaultProxyID,
		UpdateExisting:  upd,
		DefaultClientID: strings.TrimSpace(req.ClientID),
	})

	if h.refresher != nil {
		h.refresher.Kick()
	}
	if h.prober != nil {
		h.prober.Kick()
	}
	resp.OK(c, summary)
}

// splitLines 把多行文本切成 trim 后的非空行数组。
func splitLines(s string) []string {
	raw := strings.ReplaceAll(s, "\r\n", "\n")
	parts := strings.Split(raw, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func parseOAuthCallback(raw string) (code, state string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", errors.New("回调地址解析失败,请粘贴完整 URL")
	}
	q := u.Query()
	code = strings.TrimSpace(q.Get("code"))
	state = strings.TrimSpace(q.Get("state"))
	if code == "" || state == "" {
		return "", "", errors.New("回调地址缺少 code 或 state 参数")
	}
	return code, state, nil
}

// ===================== 刷新 / 探测 =====================

// POST /api/admin/accounts/:id/refresh
func (h *Handler) Refresh(c *gin.Context) {
	if h.refresher == nil {
		resp.Internal(c, "刷新器未初始化")
		return
	}
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	res, err := h.refresher.RefreshByID(c.Request.Context(), id)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, res)
}

// POST /api/admin/accounts/refresh-all
// 批量并发刷新所有账号,并返回每条结果。
func (h *Handler) RefreshAll(c *gin.Context) {
	if h.refresher == nil {
		resp.Internal(c, "刷新器未初始化")
		return
	}
	ids, err := h.svc.dao.ListAllActiveIDs(c.Request.Context())
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}

	ctx := c.Request.Context()
	conc := 8
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	results := make([]*RefreshResult, 0, len(ids))
	var mu sync.Mutex

	for _, id := range ids {
		id := id
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r, err := h.refresher.RefreshByID(ctx, id)
			if err != nil {
				r = &RefreshResult{AccountID: id, Source: "failed", Error: err.Error()}
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}()
	}
	wg.Wait()

	ok, failed := 0, 0
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			failed++
		}
	}
	resp.OK(c, gin.H{
		"total":   len(results),
		"success": ok,
		"failed":  failed,
		"results": results,
	})
}

// POST /api/admin/accounts/:id/probe-quota
func (h *Handler) ProbeQuota(c *gin.Context) {
	if h.prober == nil {
		resp.Internal(c, "额度探测器未初始化")
		return
	}
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	res, err := h.prober.ProbeByID(c.Request.Context(), id)
	if err != nil && res == nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, res)
}

// POST /api/admin/accounts/probe-quota-all
func (h *Handler) ProbeQuotaAll(c *gin.Context) {
	if h.prober == nil {
		resp.Internal(c, "额度探测器未初始化")
		return
	}
	ids, err := h.svc.dao.ListAllActiveIDs(c.Request.Context())
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}

	ctx := c.Request.Context()
	conc := 8
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	results := make([]*QuotaResult, 0, len(ids))
	var mu sync.Mutex

	for _, id := range ids {
		id := id
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r, err := h.prober.ProbeByID(ctx, id)
			if r == nil {
				r = &QuotaResult{AccountID: id}
				if err != nil {
					r.Error = err.Error()
				}
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}()
	}
	wg.Wait()

	ok, failed := 0, 0
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			failed++
		}
	}
	resp.OK(c, gin.H{
		"total":   len(results),
		"success": ok,
		"failed":  failed,
		"results": results,
	})
}
