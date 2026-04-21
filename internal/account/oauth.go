package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	openAIOAuthClientID           = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAIOAuthAuthorizeURL       = "https://auth.openai.com/oauth/authorize"
	openAIOAuthTokenURL           = "https://auth.openai.com/oauth/token"
	openAIOAuthDefaultRedirectURI = "http://localhost:1455/auth/callback"
	openAIOAuthDefaultScopes      = "openid profile email offline_access"
	openAIOAuthSessionTTL         = 30 * time.Minute
)

type openAIOAuthSession struct {
	State        string
	CodeVerifier string
	ClientID     string
	RedirectURI  string
	ProxyURL     string
	CreatedAt    time.Time
}

type openAIOAuthSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*openAIOAuthSession
}

func newOpenAIOAuthSessionStore() *openAIOAuthSessionStore {
	return &openAIOAuthSessionStore{sessions: make(map[string]*openAIOAuthSession)}
}

func (s *openAIOAuthSessionStore) Set(sessionID string, session *openAIOAuthSession) {
	if s == nil || sessionID == "" || session == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = session
}

func (s *openAIOAuthSessionStore) Get(sessionID string) (*openAIOAuthSession, bool) {
	if s == nil || sessionID == "" {
		return nil, false
	}
	s.mu.RLock()
	session, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok || session == nil {
		return nil, false
	}
	if time.Since(session.CreatedAt) > openAIOAuthSessionTTL {
		s.Delete(sessionID)
		return nil, false
	}
	return session, true
}

func (s *openAIOAuthSessionStore) Delete(sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

type openAIOAuthManager struct {
	sessions *openAIOAuthSessionStore
	tokenURL string
	now      func() time.Time
}

type openAIOAuthAuthURLResult struct {
	AuthURL     string `json:"auth_url"`
	SessionID   string `json:"session_id"`
	RedirectURI string `json:"redirect_uri"`
}

type openAIOAuthExchangeInput struct {
	SessionID   string
	Code        string
	State       string
	RedirectURI string
	ProxyURL    string
	AccountType string
}

type openAIOAuthTokenInfo struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	IDToken          string `json:"id_token,omitempty"`
	ExpiresIn        int64  `json:"expires_in"`
	ExpiresAt        int64  `json:"expires_at"`
	ClientID         string `json:"client_id"`
	Email            string `json:"email,omitempty"`
	ChatGPTAccountID string `json:"chatgpt_account_id,omitempty"`
	PlanType         string `json:"plan_type,omitempty"`
}

type openAIOAuthExchangeResult struct {
	Token  *openAIOAuthTokenInfo
	Source ImportSource
}

func newOpenAIOAuthManager() *openAIOAuthManager {
	return &openAIOAuthManager{
		sessions: newOpenAIOAuthSessionStore(),
		tokenURL: openAIOAuthTokenURL,
		now:      time.Now,
	}
}

func (m *openAIOAuthManager) GenerateAuthURL(redirectURI, proxyURL string) (*openAIOAuthAuthURLResult, error) {
	if m == nil {
		return nil, errors.New("oauth manager not initialized")
	}
	if redirectURI == "" {
		redirectURI = openAIOAuthDefaultRedirectURI
	}

	state, err := openAIOAuthRandomHex(32)
	if err != nil {
		return nil, err
	}
	sessionID, err := openAIOAuthRandomHex(16)
	if err != nil {
		return nil, err
	}
	codeVerifier, err := openAIOAuthRandomHex(64)
	if err != nil {
		return nil, err
	}

	m.sessions.Set(sessionID, &openAIOAuthSession{
		State:        state,
		CodeVerifier: codeVerifier,
		ClientID:     openAIOAuthClientID,
		RedirectURI:  redirectURI,
		ProxyURL:     proxyURL,
		CreatedAt:    m.now(),
	})

	return &openAIOAuthAuthURLResult{
		AuthURL:     buildOpenAIOAuthURL(state, codeVerifier, redirectURI),
		SessionID:   sessionID,
		RedirectURI: redirectURI,
	}, nil
}

func (m *openAIOAuthManager) ExchangeCode(ctx context.Context, in *openAIOAuthExchangeInput) (*openAIOAuthExchangeResult, error) {
	if m == nil {
		return nil, errors.New("oauth manager not initialized")
	}
	if in == nil {
		return nil, errors.New("oauth input is required")
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return nil, errors.New("session_id 不能为空")
	}
	if strings.TrimSpace(in.Code) == "" {
		return nil, errors.New("code 不能为空")
	}
	session, ok := m.sessions.Get(strings.TrimSpace(in.SessionID))
	if !ok {
		return nil, errors.New("oauth 会话不存在或已过期,请重新生成授权链接")
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(in.State)), []byte(session.State)) != 1 {
		return nil, errors.New("oauth state 不匹配,请重新登录")
	}

	redirectURI := session.RedirectURI
	if strings.TrimSpace(in.RedirectURI) != "" {
		redirectURI = strings.TrimSpace(in.RedirectURI)
	}
	proxyURL := session.ProxyURL
	if strings.TrimSpace(in.ProxyURL) != "" {
		proxyURL = strings.TrimSpace(in.ProxyURL)
	}

	tokenInfo, err := m.exchangeToken(ctx, session.ClientID, redirectURI, session.CodeVerifier, strings.TrimSpace(in.Code), proxyURL)
	if err != nil {
		return nil, err
	}
	m.sessions.Delete(strings.TrimSpace(in.SessionID))

	email := strings.TrimSpace(tokenInfo.Email)
	accountID := strings.TrimSpace(tokenInfo.ChatGPTAccountID)
	expAt := time.Time{}
	if tokenInfo.ExpiresAt > 0 {
		expAt = time.Unix(tokenInfo.ExpiresAt, 0)
	}
	if tokenInfo.AccessToken != "" && (email == "" || accountID == "" || expAt.IsZero()) {
		atEmail, atAccountID, atExp, err := decodeATClaims(tokenInfo.AccessToken)
		if err == nil {
			if email == "" {
				email = atEmail
			}
			if accountID == "" {
				accountID = atAccountID
			}
			if expAt.IsZero() {
				expAt = atExp
			}
		}
	}
	if email == "" {
		return nil, errors.New("无法从 OAuth 返回的 token 解析出邮箱")
	}

	accountType := strings.TrimSpace(in.AccountType)
	if accountType == "" {
		accountType = "codex"
	}

	out := &openAIOAuthExchangeResult{
		Token: tokenInfo,
		Source: ImportSource{
			AccessToken:      tokenInfo.AccessToken,
			RefreshToken:     tokenInfo.RefreshToken,
			Email:            email,
			ChatGPTAccountID: accountID,
			ClientID:         tokenInfo.ClientID,
			AccountType:      accountType,
			ExpiredAt:        expAt,
		},
	}
	return out, nil
}

func (m *openAIOAuthManager) exchangeToken(
	ctx context.Context,
	clientID, redirectURI, codeVerifier, code, proxyURL string,
) (*openAIOAuthTokenInfo, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", strings.TrimSpace(clientID))
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-cli/0.91.0")

	resp, err := buildImportHTTPClient(proxyURL).Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth 请求失败:%s", friendlyImportErr(err))
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oauth 换 token 失败:http=%d body=%s", resp.StatusCode, truncate(string(data), 200))
	}

	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("oauth 响应解析失败:%w", err)
	}
	if out.AccessToken == "" {
		return nil, errors.New("oauth 响应缺少 access_token")
	}

	info := &openAIOAuthTokenInfo{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		IDToken:      out.IDToken,
		ExpiresIn:    out.ExpiresIn,
		ExpiresAt:    m.now().Unix() + out.ExpiresIn,
		ClientID:     strings.TrimSpace(clientID),
	}
	if info.ClientID == "" {
		info.ClientID = openAIOAuthClientID
	}
	if out.IDToken != "" {
		if claims, err := parseOpenAIIDToken(out.IDToken); err == nil {
			info.Email = strings.TrimSpace(claims.Email)
			if claims.OpenAIAuth != nil {
				info.ChatGPTAccountID = strings.TrimSpace(claims.OpenAIAuth.ChatGPTAccountID)
				info.PlanType = normalizePlanType(claims.OpenAIAuth.ChatGPTPlanType)
			}
		}
	}
	return info, nil
}

func buildOpenAIOAuthURL(state, codeVerifier, redirectURI string) string {
	if redirectURI == "" {
		redirectURI = openAIOAuthDefaultRedirectURI
	}
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", openAIOAuthClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", openAIOAuthDefaultScopes)
	params.Set("state", state)
	params.Set("code_challenge", openAIOAuthCodeChallenge(codeVerifier))
	params.Set("code_challenge_method", "S256")
	params.Set("id_token_add_organizations", "true")
	params.Set("codex_cli_simplified_flow", "true")
	return openAIOAuthAuthorizeURL + "?" + params.Encode()
}

func openAIOAuthCodeChallenge(codeVerifier string) string {
	sum := sha256.Sum256([]byte(codeVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func openAIOAuthRandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

type openAIIDTokenClaims struct {
	Email      string                 `json:"email"`
	OpenAIAuth *openAIIDTokenOpenAuth `json:"https://api.openai.com/auth,omitempty"`
}

type openAIIDTokenOpenAuth struct {
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	ChatGPTPlanType  string `json:"chatgpt_plan_type"`
}

func parseOpenAIIDToken(idToken string) (*openAIIDTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("非法 id_token")
	}
	payload, err := decodeOpenAIJWTPayload(parts[1])
	if err != nil {
		return nil, err
	}
	var claims openAIIDTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("解析 id_token 失败:%w", err)
	}
	return &claims, nil
}

func decodeOpenAIJWTPayload(seg string) ([]byte, error) {
	if out, err := base64.RawURLEncoding.DecodeString(seg); err == nil {
		return out, nil
	}
	if m := len(seg) % 4; m != 0 {
		seg += strings.Repeat("=", 4-m)
	}
	if out, err := base64.URLEncoding.DecodeString(seg); err == nil {
		return out, nil
	}
	out, err := base64.StdEncoding.DecodeString(seg)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func normalizePlanType(plan string) string {
	plan = strings.ToLower(strings.TrimSpace(plan))
	switch plan {
	case "plus", "team", "free", "pro", "business", "enterprise", "codex":
		return plan
	default:
		return ""
	}
}
