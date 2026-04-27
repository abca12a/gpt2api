package chatgpt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUploadFileRetriesTransientPUT(t *testing.T) {
	var putCalls int32
	var uploadedCalls int32
	var registered atomic.Bool

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/files":
			if r.Method != http.MethodPost {
				t.Fatalf("create-file method = %s, want POST", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"file_id":"file_test","upload_url":"https://upload.example.test/raw","status":"success"}`))
		case "/backend-api/files/file_test/uploaded":
			if r.Method != http.MethodPost {
				t.Fatalf("uploaded method = %s, want POST", r.Method)
			}
			if atomic.AddInt32(&uploadedCalls, 1) == 1 {
				http.Error(w, "try again", http.StatusServiceUnavailable)
				return
			}
			registered.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","download_url":"https://download.example.test/file.png"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	c := &Client{
		opts: Options{
			BaseURL:       api.URL,
			AuthToken:     "token",
			DeviceID:      "device",
			UserAgent:     DefaultUserAgent,
			ClientVersion: DefaultClientVersion,
			Language:      DefaultLanguage,
		},
		hc: api.Client(),
		uploadHC: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPut {
				t.Fatalf("upload method = %s, want PUT", req.Method)
			}
			if req.URL.Host != "upload.example.test" {
				t.Fatalf("upload host = %s, want upload.example.test", req.URL.Host)
			}
			if atomic.AddInt32(&putCalls, 1) == 1 {
				return nil, io.ErrUnexpectedEOF
			}
			return &http.Response{
				StatusCode: http.StatusCreated,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		})},
	}

	got, err := c.UploadFile(context.Background(), []byte("fake image bytes"), "ref.png")
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if got.FileID != "file_test" || got.DownloadURL != "https://download.example.test/file.png" {
		t.Fatalf("unexpected upload result: %#v", got)
	}
	if atomic.LoadInt32(&putCalls) != 2 {
		t.Fatalf("put calls = %d, want 2", putCalls)
	}
	if atomic.LoadInt32(&uploadedCalls) != 2 {
		t.Fatalf("uploaded calls = %d, want 2", uploadedCalls)
	}
	if !registered.Load() {
		t.Fatal("uploaded registration was not called")
	}
}

func TestUploadFileRetriesTransientCreateFile(t *testing.T) {
	var createCalls int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/files":
			if atomic.AddInt32(&createCalls, 1) == 1 {
				http.Error(w, "try again", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"file_id":"file_test","upload_url":"https://upload.example.test/raw","status":"success"}`))
		case "/backend-api/files/file_test/uploaded":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","download_url":"https://download.example.test/file.png"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	client := &Client{
		opts: Options{
			BaseURL:       api.URL,
			AuthToken:     "token",
			DeviceID:      "device",
			UserAgent:     DefaultUserAgent,
			ClientVersion: DefaultClientVersion,
			Language:      DefaultLanguage,
		},
		hc: api.Client(),
		uploadHC: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusCreated,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		})},
	}

	if _, err := client.UploadFile(context.Background(), []byte("fake image bytes"), "ref.png"); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if atomic.LoadInt32(&createCalls) != 2 {
		t.Fatalf("create calls = %d, want 2", createCalls)
	}
}
