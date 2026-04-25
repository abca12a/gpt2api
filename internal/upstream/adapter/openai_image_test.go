package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIImageGeneratePassesGPTImageOutputParameters(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("path = %s, want /v1/images/generations", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Fatalf("authorization = %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"abc"}]}`))
	}))
	defer srv.Close()

	compression := 50
	a := NewOpenAI(Params{BaseURL: srv.URL, APIKey: "test-key"})
	_, err := a.ImageGenerate(context.Background(), "gpt-image-2", &ImageRequest{
		Prompt:            "draw",
		N:                 2,
		Size:              "3840x2160",
		Quality:           "high",
		Format:            "url",
		OutputFormat:      "jpeg",
		OutputCompression: &compression,
		Background:        "auto",
		Moderation:        "low",
	})
	if err != nil {
		t.Fatalf("ImageGenerate: %v", err)
	}

	want := map[string]any{
		"model":              "gpt-image-2",
		"prompt":             "draw",
		"n":                  float64(2),
		"size":               "3840x2160",
		"quality":            "high",
		"output_format":      "jpeg",
		"output_compression": float64(50),
		"background":         "auto",
		"moderation":         "low",
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("payload[%s] = %#v, want %#v; payload=%#v", key, got[key], value, got)
		}
	}
	if _, ok := got["response_format"]; ok {
		t.Fatalf("response_format should be omitted for GPT Image models: %#v", got)
	}
}

func TestOpenAIImageGenerateKeepsLegacyResponseFormatForDalle(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"url":"https://example.test/image.png"}]}`))
	}))
	defer srv.Close()

	a := NewOpenAI(Params{BaseURL: srv.URL, APIKey: "test-key"})
	_, err := a.ImageGenerate(context.Background(), "dall-e-3", &ImageRequest{
		Prompt: "draw",
		Format: "url",
	})
	if err != nil {
		t.Fatalf("ImageGenerate: %v", err)
	}
	if got["response_format"] != "url" {
		t.Fatalf("response_format = %#v, want url; payload=%#v", got["response_format"], got)
	}
}
