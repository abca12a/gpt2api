package chatgpt

import "testing"

func TestChatRequirementsLogSummaryDoesNotExposeTokenOrRawBody(t *testing.T) {
	body := []byte(`{"persona":"chatgpt-freeaccount","token":"gAAAAABsecret-value","proofofwork":{"required":true,"seed":"seed-secret","difficulty":"0fffff"},"turnstile":{"required":true}}`)
	var resp ChatRequirementsResp
	resp.Token = "gAAAAABsecret-value"
	resp.Persona = "chatgpt-freeaccount"
	resp.Proofofwork.Required = true
	resp.Proofofwork.Seed = "seed-secret"
	resp.Proofofwork.Difficulty = "0fffff"
	resp.Turnstile.Required = true

	fields := chatRequirementsLogFields(body, resp)
	values := map[string]any{}
	for _, field := range fields {
		values[field.Key] = field.Interface
		if field.String != "" {
			values[field.Key] = field.String
		}
		if field.Integer != 0 {
			values[field.Key] = field.Integer
		}
	}

	if _, ok := values["body"]; ok {
		t.Fatal("log fields must not include raw response body")
	}
	if _, ok := values["token_prefix"]; ok {
		t.Fatal("log fields must not include token prefix")
	}
	if got := values["token_len"]; got != int64(len(resp.Token)) {
		t.Fatalf("token_len = %v, want %d", got, len(resp.Token))
	}
	if got := values["body_len"]; got != int64(len(body)) {
		t.Fatalf("body_len = %v, want %d", got, len(body))
	}
}
