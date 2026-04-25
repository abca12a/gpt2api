package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	imagepkg "github.com/432539/gpt2api/internal/image"
	"github.com/gin-gonic/gin"
)

func TestShouldWaitForImageResultAsyncCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name   string
		target string
		header map[string]string
		req    ImageGenRequest
		want   bool
	}{
		{name: "default sync", target: "/v1/images/generations", want: true},
		{name: "query async true", target: "/v1/images/generations?async=true", want: false},
		{name: "query wait false", target: "/v1/images/generations?wait_for_result=false", want: false},
		{name: "prefer respond async", target: "/v1/images/generations", header: map[string]string{"Prefer": "respond-async"}, want: false},
		{name: "body wait false", target: "/v1/images/generations", req: ImageGenRequest{WaitForResult: boolPtr(false)}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			req := httptest.NewRequest(http.MethodPost, tt.target, nil)
			for k, v := range tt.header {
				req.Header.Set(k, v)
			}
			c.Request = req

			if got := shouldWaitForImageResult(c, tt.req); got != tt.want {
				t.Fatalf("shouldWaitForImageResult() = %v, want %v", got, tt.want)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }

func TestAsyncImageSubmissionUsesOKForUpstreamGatewayCompatibility(t *testing.T) {
	if asyncImageSubmitStatusCode() != http.StatusOK {
		t.Fatalf("async submit status = %d, want %d", asyncImageSubmitStatusCode(), http.StatusOK)
	}
}

func TestAsyncImageRunTuningUsesFastNoReferenceDefaults(t *testing.T) {
	attempts, perAttempt, pollMaxWait, dispatchTimeout := asyncImageRunTuning(0, false)
	if attempts != 5 {
		t.Fatalf("attempts = %d, want 5", attempts)
	}
	if perAttempt != 90*time.Second {
		t.Fatalf("perAttempt = %s, want 90s", perAttempt)
	}
	if pollMaxWait != 60*time.Second {
		t.Fatalf("pollMaxWait = %s, want 60s", pollMaxWait)
	}
	if dispatchTimeout != 10*time.Second {
		t.Fatalf("dispatchTimeout = %s, want 10s", dispatchTimeout)
	}
}

func TestAsyncImageRunTuningCapsNoReferenceAttempts(t *testing.T) {
	attempts, _, _, _ := asyncImageRunTuning(10, false)
	if attempts != 5 {
		t.Fatalf("attempts = %d, want 5", attempts)
	}
}

func TestAsyncImageTaskTimeoutUsesTunedNoReferenceWindow(t *testing.T) {
	if got := asyncImageTaskTimeout(0, false); got != 5*time.Minute {
		t.Fatalf("asyncImageTaskTimeout = %s, want 5m", got)
	}
}

func TestNormalizeChatImageRequestPreservesImageParameters(t *testing.T) {
	compression := 50
	req := &ChatCompletionsRequest{
		Model:             "gpt-image-2",
		N:                 9,
		Size:              "3840x2160",
		Quality:           "high",
		ResponseFormat:    "url",
		OutputFormat:      "jpeg",
		OutputCompression: &compression,
		Background:        "auto",
		Moderation:        "low",
	}

	got := normalizeChatImageRequest("draw", req)
	if got.Prompt != "draw" || got.N != 4 || got.Size != "3840x2160" || got.Upscale != imagepkg.Upscale4K {
		t.Fatalf("unexpected normalized request: %#v", got)
	}
	if got.Quality != "high" || got.ResponseFormat != "url" || got.OutputFormat != "jpeg" || got.OutputCompression == nil || *got.OutputCompression != 50 || got.Background != "auto" || got.Moderation != "low" {
		t.Fatalf("parameters not preserved: %#v", got)
	}
}

func TestNormalizeImageUpscaleInfers2KAnd4KFromSize(t *testing.T) {
	if got := normalizeImageUpscale("1536x1024", ""); got != imagepkg.UpscaleNone {
		t.Fatalf("1536x1024 inferred upscale = %q, want none", got)
	}
	if got := normalizeImageUpscale("2048x2048", ""); got != imagepkg.Upscale2K {
		t.Fatalf("2048x2048 inferred upscale = %q, want 2k", got)
	}
	if got := normalizeImageUpscale("2560x1440", ""); got != imagepkg.Upscale2K {
		t.Fatalf("2560x1440 inferred upscale = %q, want 2k", got)
	}
	if got := normalizeImageUpscale("2160x3840", ""); got != imagepkg.Upscale4K {
		t.Fatalf("2160x3840 inferred upscale = %q, want 4k", got)
	}
	if got := normalizeImageUpscale("3840x2160", "2k"); got != imagepkg.Upscale2K {
		t.Fatalf("explicit upscale should win, got %q", got)
	}
	if got := normalizeImageUpscale("1024x1024", " 4K "); got != imagepkg.Upscale4K {
		t.Fatalf("explicit uppercase upscale = %q, want 4k", got)
	}
}

func TestRequestedUpscaleFromAliases(t *testing.T) {
	if got := requestedUpscaleFromOptions("", "UHD"); got != imagepkg.Upscale4K {
		t.Fatalf("UHD alias = %q, want 4k", got)
	}
	if got := requestedUpscaleFromOptions("", "2160p"); got != imagepkg.Upscale4K {
		t.Fatalf("2160p alias = %q, want 4k", got)
	}
	if got := requestedUpscaleFromOptions("", "2K"); got != imagepkg.Upscale2K {
		t.Fatalf("2K alias = %q, want 2k", got)
	}
	if got := requestedUpscaleFromOptions("", "high"); got != imagepkg.UpscaleNone {
		t.Fatalf("high should not imply upscale, got %q", got)
	}
}

func TestImageRequestForChannelMapsResolutionRatiosToNativeSize(t *testing.T) {
	req := &ImageGenRequest{Size: "16:9", Resolution: "4k", Quality: "high"}
	got := imageRequestForChannel(req, requestedUpscaleFromOptions(req.Upscale, req.Resolution, req.ImageSize, req.Scale, req.Quality))
	if got == req {
		t.Fatal("imageRequestForChannel should return a copy")
	}
	if got.Size != "3840x2160" {
		t.Fatalf("16:9 4k size = %q, want 3840x2160", got.Size)
	}
	if got.Quality != "high" || req.Size != "16:9" {
		t.Fatalf("unexpected mutation or quality: got=%#v original=%#v", got, req)
	}

	req = &ImageGenRequest{Size: "9:16", Resolution: "4k"}
	got = imageRequestForChannel(req, requestedUpscaleFromOptions(req.Upscale, req.Resolution, req.ImageSize, req.Scale, req.Quality))
	if got.Size != "2160x3840" {
		t.Fatalf("9:16 4k size = %q, want 2160x3840", got.Size)
	}

	req = &ImageGenRequest{Size: "2:3", Resolution: "4k"}
	got = imageRequestForChannel(req, requestedUpscaleFromOptions(req.Upscale, req.Resolution, req.ImageSize, req.Scale, req.Quality))
	if got.Size != "2336x3504" {
		t.Fatalf("2:3 4k size = %q, want 2336x3504", got.Size)
	}

	req = &ImageGenRequest{Size: "1:1", Resolution: "4k"}
	got = imageRequestForChannel(req, requestedUpscaleFromOptions(req.Upscale, req.Resolution, req.ImageSize, req.Scale, req.Quality))
	if got.Size != "2880x2880" {
		t.Fatalf("1:1 4k size = %q, want 2880x2880", got.Size)
	}
}

func TestImageRequestForChannelMaps2KAnd1KRatios(t *testing.T) {
	req := &ImageGenRequest{Size: "16:9", Resolution: "2k"}
	got := imageRequestForChannel(req, requestedUpscaleFromOptions(req.Upscale, req.Resolution, req.ImageSize, req.Scale, req.Quality))
	if got.Size != "2048x1152" {
		t.Fatalf("16:9 2k size = %q, want 2048x1152", got.Size)
	}

	req = &ImageGenRequest{Size: "16:9", Resolution: "1k"}
	got = imageRequestForChannel(req, requestedUpscaleFromOptions(req.Upscale, req.Resolution, req.ImageSize, req.Scale, req.Quality))
	if got.Size != "1024x576" {
		t.Fatalf("16:9 1k size = %q, want 1024x576", got.Size)
	}

	req = &ImageGenRequest{Size: "1024x1536", Resolution: "4k"}
	got = imageRequestForChannel(req, requestedUpscaleFromOptions(req.Upscale, req.Resolution, req.ImageSize, req.Scale, req.Quality))
	if got.Size != "1024x1536" {
		t.Fatalf("pixel size should be preserved, got %q", got.Size)
	}
}

func TestImageRequestForChannelSanitizesQualityResolutionAlias(t *testing.T) {
	req := &ImageGenRequest{Size: "16:9", Quality: "4K", OutputFormat: "png"}
	got := imageRequestForChannel(req, requestedUpscaleFromOptions(req.Upscale, req.Resolution, req.ImageSize, req.Scale, req.Quality))
	if got.Size != "3840x2160" {
		t.Fatalf("quality alias size = %q, want 3840x2160", got.Size)
	}
	if got.Quality != "" {
		t.Fatalf("quality alias should be stripped before channel dispatch, got %q", got.Quality)
	}
	if req.Quality != "4K" {
		t.Fatalf("original request mutated: %#v", req)
	}
}

func TestImageAdapterRequestIncludesReferenceDataURLs(t *testing.T) {
	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\b\x02\x00\x00\x00")
	got := imageAdapterRequest(nil, &ImageGenRequest{Prompt: "edit", Size: "1024x1024"}, []imagepkg.ReferenceImage{
		{Data: pngBytes},
	})
	if len(got.Images) != 1 {
		t.Fatalf("Images len = %d, want 1", len(got.Images))
	}
	if !strings.HasPrefix(got.Images[0], "data:image/png;base64,") {
		t.Fatalf("unexpected data URL: %q", got.Images[0])
	}
}

func TestImageGenRequestReferenceAliases(t *testing.T) {
	var req ImageGenRequest
	body := []byte(`{
		"reference_images": "https://example.test/a.png",
		"images": ["data:image/png;base64,bbb"],
		"image_url": {"url":"https://example.test/c.png"},
		"input_images": [{"url":"https://example.test/d.png"}]
	}`)
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal image request: %v", err)
	}
	refs := req.referenceInputs()
	if len(refs) != 4 {
		t.Fatalf("referenceInputs len = %d, want 4: %#v", len(refs), refs)
	}
	if refs[0] != "https://example.test/a.png" || refs[2] != "https://example.test/c.png" {
		t.Fatalf("unexpected refs: %#v", refs)
	}
}

func TestBuildImageTaskCompatPayloadSuccess(t *testing.T) {
	created := time.Unix(1777040000, 0)
	finished := created.Add(time.Minute)
	task := &imagepkg.Task{
		TaskID:     "img_abc",
		Status:     imagepkg.StatusSuccess,
		FileIDs:    []byte(`["file_123"]`),
		ResultURLs: []byte(`["https://upstream.example/image.png"]`),
		CreatedAt:  created,
		FinishedAt: &finished,
	}

	body, err := json.Marshal(buildImageTaskCompatPayload(task))
	if err != nil {
		t.Fatalf("marshal compat payload: %v", err)
	}

	var got struct {
		Object      string `json:"object"`
		Status      string `json:"status"`
		Progress    int    `json:"progress"`
		Error       any    `json:"error"`
		CompletedAt int64  `json:"completed_at"`
		Result      struct {
			Data []ImageGenData `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal compat payload: %v", err)
	}
	if got.Object != "image.task" || got.Status != "succeeded" || got.Progress != 100 {
		t.Fatalf("unexpected task envelope: object=%q status=%q progress=%d", got.Object, got.Status, got.Progress)
	}
	if got.Error != nil {
		t.Fatalf("success payload should not include error, got %#v", got.Error)
	}
	if got.CompletedAt != finished.Unix() {
		t.Fatalf("completed_at = %d, want %d", got.CompletedAt, finished.Unix())
	}
	if len(got.Result.Data) != 1 || got.Result.Data[0].URL == "" || got.Result.Data[0].FileID != "file_123" {
		t.Fatalf("unexpected result data: %#v", got.Result.Data)
	}
}

func TestBuildImageTaskCompatPayloadFallsBackToDirectResultURL(t *testing.T) {
	task := &imagepkg.Task{
		TaskID:     "img_channel",
		Status:     imagepkg.StatusSuccess,
		ResultURLs: []byte(`["data:image/png;base64,abc"]`),
		CreatedAt:  time.Unix(1777040000, 0),
	}

	body, err := json.Marshal(buildImageTaskCompatPayload(task))
	if err != nil {
		t.Fatalf("marshal compat payload: %v", err)
	}

	var got struct {
		Result struct {
			Data []ImageGenData `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal compat payload: %v", err)
	}
	if len(got.Result.Data) != 1 || got.Result.Data[0].URL != "data:image/png;base64,abc" {
		t.Fatalf("direct result URL not preserved: %#v", got.Result.Data)
	}
}

func TestBuildImageTaskCompatPayloadFailureUsesErrorObject(t *testing.T) {
	task := &imagepkg.Task{
		TaskID:    "img_failed",
		Status:    imagepkg.StatusFailed,
		Error:     imagepkg.ErrPollTimeout,
		CreatedAt: time.Unix(1777040000, 0),
	}

	body, err := json.Marshal(buildImageTaskCompatPayload(task))
	if err != nil {
		t.Fatalf("marshal compat payload: %v", err)
	}

	var got struct {
		Status string `json:"status"`
		Error  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal compat payload: %v", err)
	}
	if got.Status != "failed" || got.Error.Code != imagepkg.ErrPollTimeout || got.Error.Message == "" {
		t.Fatalf("unexpected failure payload: %#v", got)
	}
}
