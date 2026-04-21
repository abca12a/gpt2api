package account

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestOpenAIOAuthGenerateAuthURL(t *testing.T) {
	t.Parallel()

	mgr := newOpenAIOAuthManager()
	res, err := mgr.GenerateAuthURL("", "http://proxy.example:7890")
	if err != nil {
		t.Fatalf("GenerateAuthURL() error = %v", err)
	}
	if res == nil {
		t.Fatal("GenerateAuthURL() returned nil result")
	}
	if res.SessionID == "" {
		t.Fatal("session_id should not be empty")
	}
	if res.RedirectURI != openAIOAuthDefaultRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", res.RedirectURI, openAIOAuthDefaultRedirectURI)
	}

	u, err := url.Parse(res.AuthURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	q := u.Query()
	if got := q.Get("response_type"); got != "code" {
		t.Fatalf("response_type = %q, want code", got)
	}
	if got := q.Get("client_id"); got != openAIOAuthClientID {
		t.Fatalf("client_id = %q, want %q", got, openAIOAuthClientID)
	}
	if got := q.Get("redirect_uri"); got != openAIOAuthDefaultRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, openAIOAuthDefaultRedirectURI)
	}
	if got := q.Get("scope"); got != openAIOAuthDefaultScopes {
		t.Fatalf("scope = %q, want %q", got, openAIOAuthDefaultScopes)
	}
	if got := q.Get("state"); got == "" {
		t.Fatal("state should not be empty")
	}
	if got := q.Get("code_challenge"); got == "" {
		t.Fatal("code_challenge should not be empty")
	}
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", got)
	}
	if got := q.Get("id_token_add_organizations"); got != "true" {
		t.Fatalf("id_token_add_organizations = %q, want true", got)
	}
	if got := q.Get("codex_cli_simplified_flow"); got != "true" {
		t.Fatalf("codex_cli_simplified_flow = %q, want true", got)
	}

	sess, ok := mgr.sessions.Get(res.SessionID)
	if !ok {
		t.Fatal("session should be stored after GenerateAuthURL")
	}
	if sess.ProxyURL != "http://proxy.example:7890" {
		t.Fatalf("session proxy = %q, want %q", sess.ProxyURL, "http://proxy.example:7890")
	}
	if sess.CodeVerifier == "" {
		t.Fatal("code_verifier should not be empty")
	}
}

func TestOpenAIOAuthExchangeCode(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	mgr := newOpenAIOAuthManager()
	mgr.now = func() time.Time { return now }

	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  makeJWT(map[string]any{"email": "fallback@example.com", "chatgpt_account_id": "acc-fallback", "exp": now.Add(2 * time.Hour).Unix()}),
			"refresh_token": "rt-openai-123",
			"id_token": makeJWT(map[string]any{
				"email": "oauth@example.com",
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_account_id": "acc-123",
					"chatgpt_plan_type":  "plus",
				},
			}),
			"expires_in": 7200,
		})
	}))
	defer srv.Close()
	mgr.tokenURL = srv.URL

	mgr.sessions.Set("sess-1", &openAIOAuthSession{
		State:        "state-123",
		CodeVerifier: "verifier-456",
		ClientID:     openAIOAuthClientID,
		RedirectURI:  openAIOAuthDefaultRedirectURI,
		CreatedAt:    time.Now(),
	})

	out, err := mgr.ExchangeCode(context.Background(), &openAIOAuthExchangeInput{
		SessionID:   "sess-1",
		Code:        "code-xyz",
		State:       "state-123",
		AccountType: "codex",
	})
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}
	if got := gotForm.Get("grant_type"); got != "authorization_code" {
		t.Fatalf("grant_type = %q, want authorization_code", got)
	}
	if got := gotForm.Get("client_id"); got != openAIOAuthClientID {
		t.Fatalf("client_id = %q, want %q", got, openAIOAuthClientID)
	}
	if got := gotForm.Get("code"); got != "code-xyz" {
		t.Fatalf("code = %q, want %q", got, "code-xyz")
	}
	if got := gotForm.Get("redirect_uri"); got != openAIOAuthDefaultRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, openAIOAuthDefaultRedirectURI)
	}
	if got := gotForm.Get("code_verifier"); got != "verifier-456" {
		t.Fatalf("code_verifier = %q, want %q", got, "verifier-456")
	}

	if out == nil || out.Token == nil {
		t.Fatal("ExchangeCode() should return token info")
	}
	if out.Token.Email != "oauth@example.com" {
		t.Fatalf("token email = %q, want oauth@example.com", out.Token.Email)
	}
	if out.Token.ChatGPTAccountID != "acc-123" {
		t.Fatalf("token chatgpt_account_id = %q, want acc-123", out.Token.ChatGPTAccountID)
	}
	if out.Token.PlanType != "plus" {
		t.Fatalf("token plan_type = %q, want plus", out.Token.PlanType)
	}
	if out.Token.ExpiresAt != now.Unix()+7200 {
		t.Fatalf("token expires_at = %d, want %d", out.Token.ExpiresAt, now.Unix()+7200)
	}
	if out.Source.Email != "oauth@example.com" {
		t.Fatalf("source email = %q, want oauth@example.com", out.Source.Email)
	}
	if out.Source.RefreshToken != "rt-openai-123" {
		t.Fatalf("source refresh_token = %q, want rt-openai-123", out.Source.RefreshToken)
	}
	if out.Source.ChatGPTAccountID != "acc-123" {
		t.Fatalf("source chatgpt_account_id = %q, want acc-123", out.Source.ChatGPTAccountID)
	}
	if out.Source.AccountType != "codex" {
		t.Fatalf("source account_type = %q, want codex", out.Source.AccountType)
	}
	if out.Source.ClientID != openAIOAuthClientID {
		t.Fatalf("source client_id = %q, want %q", out.Source.ClientID, openAIOAuthClientID)
	}
	if out.Source.ExpiredAt.Unix() != now.Add(2*time.Hour).Unix() {
		t.Fatalf("source expired_at = %d, want %d", out.Source.ExpiredAt.Unix(), now.Add(2*time.Hour).Unix())
	}

	if _, ok := mgr.sessions.Get("sess-1"); ok {
		t.Fatal("session should be deleted after successful exchange")
	}
}

func TestOpenAIOAuthExchangeCodeRejectsInvalidState(t *testing.T) {
	t.Parallel()

	mgr := newOpenAIOAuthManager()
	mgr.sessions.Set("sess-1", &openAIOAuthSession{
		State:        "state-123",
		CodeVerifier: "verifier-456",
		ClientID:     openAIOAuthClientID,
		RedirectURI:  openAIOAuthDefaultRedirectURI,
		CreatedAt:    time.Now(),
	})

	_, err := mgr.ExchangeCode(context.Background(), &openAIOAuthExchangeInput{
		SessionID: "sess-1",
		Code:      "code-xyz",
		State:     "wrong-state",
	})
	if err == nil {
		t.Fatal("ExchangeCode() should reject invalid state")
	}
	if !strings.Contains(err.Error(), "state") {
		t.Fatalf("error = %v, want state-related error", err)
	}
}

func makeJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	body, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(body)
	return header + "." + payload + ".sig"
}
