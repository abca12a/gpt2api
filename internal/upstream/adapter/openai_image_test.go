package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testImageObserver struct {
	submit time.Duration
	poll   time.Duration
}

func (o *testImageObserver) RecordSubmitDuration(d time.Duration) { o.submit += d }
func (o *testImageObserver) RecordPollDuration(d time.Duration)   { o.poll += d }

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

func TestOpenAIImageGenerateUsesEditsEndpointForReferenceImages(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" {
			t.Fatalf("path = %s, want /v1/images/edits", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"abc"}]}`))
	}))
	defer srv.Close()

	a := NewOpenAI(Params{BaseURL: srv.URL, APIKey: "test-key"})
	_, err := a.ImageGenerate(context.Background(), "gpt-image-2", &ImageRequest{
		Prompt: "edit",
		Size:   "2048x1152",
		Images: []string{"data:image/png;base64,aaa", "data:image/jpeg;base64,bbb"},
	})
	if err != nil {
		t.Fatalf("ImageGenerate: %v", err)
	}

	if got["model"] != "gpt-image-2" || got["prompt"] != "edit" || got["size"] != "2048x1152" {
		t.Fatalf("unexpected payload basics: %#v", got)
	}
	images, ok := got["images"].([]any)
	if !ok || len(images) != 2 {
		t.Fatalf("images = %#v, want 2 entries", got["images"])
	}
	first, ok := images[0].(map[string]any)
	if !ok || first["image_url"] != "data:image/png;base64,aaa" {
		t.Fatalf("first image = %#v", images[0])
	}
	if _, ok := got["response_format"]; ok {
		t.Fatalf("response_format should be omitted for GPT Image edits: %#v", got)
	}
}

func TestOpenAIImageGenerateUsesAPIMartGenerationsForReferenceImages(t *testing.T) {
	var (
		taskPolls int
		payload   map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apimart.ai/v1/images/generations":
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":[{"status":"submitted","task_id":"task_ref_123"}]}`))
		case "/apimart.ai/v1/tasks/task_ref_123":
			taskPolls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":{"status":"completed","result":{"images":[{"url":["https://example.test/apimart-ref.png"]}]}}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	a := NewOpenAI(Params{
		BaseURL: srv.URL + "/apimart.ai",
		APIKey:  "test-key",
		Extra:   `{"official_fallback":true}`,
	})
	result, err := a.ImageGenerate(context.Background(), "gpt-image-2", &ImageRequest{
		Prompt:      "edit",
		N:           1,
		Size:        "1024x1024",
		AspectRatio: "1:1",
		Resolution:  "1k",
		Images:      []string{"data:image/png;base64,aaa", "https://example.test/ref.jpg"},
	})
	if err != nil {
		t.Fatalf("ImageGenerate: %v", err)
	}
	if result == nil || len(result.URLs) != 1 || result.URLs[0] != "https://example.test/apimart-ref.png" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if taskPolls != 1 {
		t.Fatalf("task polls = %d, want 1", taskPolls)
	}
	if payload["size"] != "1:1" {
		t.Fatalf("payload size = %#v, want 1:1", payload["size"])
	}
	if payload["resolution"] != "1k" {
		t.Fatalf("payload resolution = %#v, want 1k", payload["resolution"])
	}
	if payload["official_fallback"] != true {
		t.Fatalf("payload official_fallback = %#v, want true", payload["official_fallback"])
	}
	if _, ok := payload["images"]; ok {
		t.Fatalf("payload should not contain images for apimart reference mode: %#v", payload)
	}
	imageURLs, ok := payload["image_urls"].([]any)
	if !ok || len(imageURLs) != 2 {
		t.Fatalf("image_urls = %#v, want 2 entries", payload["image_urls"])
	}
	if imageURLs[0] != "data:image/png;base64,aaa" || imageURLs[1] != "https://example.test/ref.jpg" {
		t.Fatalf("unexpected image_urls: %#v", imageURLs)
	}
}

func TestOpenAIImageGenerateClassifiesContentPolicyViolation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"content_policy_violation","message":"Your request was rejected as a result of our safety system.","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	a := NewOpenAI(Params{BaseURL: srv.URL, APIKey: "test-key"})
	_, err := a.ImageGenerate(context.Background(), "gpt-image-2", &ImageRequest{Prompt: "blocked"})
	if err == nil {
		t.Fatal("expected image generation error")
	}
	if !IsContentModerationError(err) {
		t.Fatalf("expected content moderation error, got %v", err)
	}
	if !strings.Contains(err.Error(), "content_policy_violation") {
		t.Fatalf("error should preserve upstream code for logs, got %v", err)
	}
}

func TestOpenAIImageGenerateClassifiesSafetySystemMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Your request was rejected as a result of our safety system.","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	a := NewOpenAI(Params{BaseURL: srv.URL, APIKey: "test-key"})
	_, err := a.ImageGenerate(context.Background(), "gpt-image-2", &ImageRequest{Prompt: "blocked"})
	if err == nil {
		t.Fatal("expected image generation error")
	}
	if !IsContentModerationError(err) {
		t.Fatalf("expected safety-system message to classify as content moderation, got %v", err)
	}
}

func TestOpenAIImageGeneratePollsAPIMartAsyncTask(t *testing.T) {
	var taskPolls int
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Fatalf("authorization = %q", auth)
		}
		switch r.URL.Path {
		case "/apimart.ai/v1/images/generations":
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":[{"status":"submitted","task_id":"task_123"}]}`))
		case "/apimart.ai/v1/tasks/task_123":
			taskPolls++
			if got := r.URL.Query().Get("language"); got != "en" {
				t.Fatalf("language query = %q, want en", got)
			}
			w.Header().Set("Content-Type", "application/json")
			if taskPolls == 1 {
				_, _ = w.Write([]byte(`{"code":200,"data":{"status":"processing","progress":40}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":200,"data":{"status":"completed","result":{"images":[{"url":["https://example.test/apimart.png"]}]}}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	a := NewOpenAI(Params{BaseURL: srv.URL + "/apimart.ai", APIKey: "test-key"})
	result, err := a.ImageGenerate(context.Background(), "gpt-image-2", &ImageRequest{
		Prompt:      "draw",
		N:           1,
		Size:        "1536x864",
		AspectRatio: "16:9",
		Resolution:  "2k",
	})
	if err != nil {
		t.Fatalf("ImageGenerate: %v", err)
	}
	if result == nil || len(result.URLs) != 1 || result.URLs[0] != "https://example.test/apimart.png" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if taskPolls != 2 {
		t.Fatalf("task polls = %d, want 2", taskPolls)
	}
	if payload["size"] != "16:9" {
		t.Fatalf("payload size = %#v, want 16:9", payload["size"])
	}
	if payload["resolution"] != "2k" {
		t.Fatalf("payload resolution = %#v, want 2k", payload["resolution"])
	}
}

func TestOpenAIImageGenerateSendsAPIMart4KPixelSizeInsteadOfUnsupportedRatio(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apimart.ai/v1/images/generations":
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":[{"status":"submitted","task_id":"task_4k_123"}]}`))
		case "/apimart.ai/v1/tasks/task_4k_123":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":{"status":"completed","result":{"images":[{"url":["https://example.test/apimart-4k.png"]}]}}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	a := NewOpenAI(Params{BaseURL: srv.URL + "/apimart.ai", APIKey: "test-key"})
	_, err := a.ImageGenerate(context.Background(), "gpt-image-2", &ImageRequest{
		Prompt:      "draw",
		Size:        "2880x2880",
		AspectRatio: "1:1",
		Resolution:  "4k",
	})
	if err != nil {
		t.Fatalf("ImageGenerate: %v", err)
	}
	if payload["size"] != "2880x2880" {
		t.Fatalf("payload size = %#v, want pixel size for 4k", payload["size"])
	}
	if payload["resolution"] != "4k" {
		t.Fatalf("payload resolution = %#v, want 4k", payload["resolution"])
	}
}

func TestOpenAIImageGenerateReportsAPIMartTimingsToObserver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apimart.ai/v1/images/generations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":[{"status":"submitted","task_id":"task_obs_123"}]}`))
		case "/apimart.ai/v1/tasks/task_obs_123":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":{"status":"completed","result":{"images":[{"url":["https://example.test/apimart-obs.png"]}]}}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	a := NewOpenAI(Params{BaseURL: srv.URL + "/apimart.ai", APIKey: "test-key"})
	observer := &testImageObserver{}
	ctx := WithImageGenerateObserver(context.Background(), observer)

	result, err := a.ImageGenerate(ctx, "gpt-image-2", &ImageRequest{Prompt: "draw"})
	if err != nil {
		t.Fatalf("ImageGenerate: %v", err)
	}
	if result == nil || len(result.URLs) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if observer.submit <= 0 {
		t.Fatalf("submit timing = %s, want > 0", observer.submit)
	}
	if observer.poll <= 0 {
		t.Fatalf("poll timing = %s, want > 0", observer.poll)
	}
}

func TestOpenAIImageGenerateReturnsTaskFailureDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/images/generations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":[{"status":"submitted","task_id":"task_456"}]}`))
		case "/v1/tasks/task_456":
			if got := r.URL.Query(); got.Get("language") != "en" {
				t.Fatalf("unexpected query: %s", got.Encode())
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":{"status":"failed","error":{"code":"content_policy_violation","type":"server_error","message":"sensitive content detected"}}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	a := NewOpenAI(Params{BaseURL: srv.URL, APIKey: "test-key"})
	_, err := a.ImageGenerate(context.Background(), "gpt-image-2", &ImageRequest{Prompt: "draw"})
	if err == nil {
		t.Fatal("expected task failure error")
	}
	var upstreamErr *UpstreamHTTPError
	if !strings.Contains(err.Error(), "content_policy_violation") {
		t.Fatalf("error should preserve task code, got %v", err)
	}
	if !IsContentModerationError(err) {
		t.Fatalf("expected task failure to classify as content moderation, got %v", err)
	}
	if !errors.As(err, &upstreamErr) {
		t.Fatalf("expected UpstreamHTTPError, got %T", err)
	}
	if upstreamErr.Status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", upstreamErr.Status)
	}
}

func TestOpenAIImageGeneratePerformsFinalAPIMartPollAfterContextDeadline(t *testing.T) {
	var taskPolls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apimart.ai/v1/images/generations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":[{"status":"submitted","task_id":"task_deadline_123"}]}`))
		case "/apimart.ai/v1/tasks/task_deadline_123":
			taskPolls++
			w.Header().Set("Content-Type", "application/json")
			if taskPolls == 1 {
				_, _ = w.Write([]byte(`{"code":200,"data":{"status":"processing","progress":95}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":200,"data":{"status":"completed","result":{"images":[{"url":["https://example.test/apimart-deadline.png"]}]}}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	a := NewOpenAI(Params{BaseURL: srv.URL + "/apimart.ai", APIKey: "test-key"})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	result, err := a.ImageGenerate(ctx, "gpt-image-2", &ImageRequest{Prompt: "draw"})
	if err != nil {
		t.Fatalf("ImageGenerate: %v", err)
	}
	if result == nil || len(result.URLs) != 1 || result.URLs[0] != "https://example.test/apimart-deadline.png" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if taskPolls != 2 {
		t.Fatalf("task polls = %d, want 2", taskPolls)
	}
}

func TestOpenAIImageGenerateKeepsDeadlineWhenFinalAPIMartPollStillPending(t *testing.T) {
	var taskPolls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apimart.ai/v1/images/generations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":[{"status":"submitted","task_id":"task_deadline_pending"}]}`))
		case "/apimart.ai/v1/tasks/task_deadline_pending":
			taskPolls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":{"status":"processing","progress":95}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	a := NewOpenAI(Params{BaseURL: srv.URL + "/apimart.ai", APIKey: "test-key"})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := a.ImageGenerate(ctx, "gpt-image-2", &ImageRequest{Prompt: "draw"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if taskPolls != 2 {
		t.Fatalf("task polls = %d, want 2", taskPolls)
	}
}
