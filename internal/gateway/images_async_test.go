package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
