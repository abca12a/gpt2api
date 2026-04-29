package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchReferenceBytesRetriesTransientHTTPFailure(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	defer srv.Close()

	data, name, err := fetchReferenceBytes(context.Background(), srv.URL+"/ref.png")
	if err != nil {
		t.Fatalf("fetchReferenceBytes() error = %v", err)
	}
	if got := string(data); got != "png" {
		t.Fatalf("body = %q, want %q", got, "png")
	}
	if name != "ref.png" {
		t.Fatalf("name = %q, want ref.png", name)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestFetchReferenceBytesRetriesBrokenBodyRead(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				panic("response writer does not support hijack")
			}
			conn, buf, err := hj.Hijack()
			if err != nil {
				panic(err)
			}
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: image/png\r\nContent-Length: 6\r\nConnection: close\r\n\r\nabc")
			_ = buf.Flush()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("abcdef"))
	}))
	defer srv.Close()

	data, name, err := fetchReferenceBytes(context.Background(), srv.URL+"/broken.png")
	if err != nil {
		t.Fatalf("fetchReferenceBytes() error = %v", err)
	}
	if got := string(data); got != "abcdef" {
		t.Fatalf("body = %q, want %q", got, "abcdef")
	}
	if name != "broken.png" {
		t.Fatalf("name = %q, want broken.png", name)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestFetchReferenceBytesDoesNotRetryClientFailure(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer srv.Close()

	_, _, err := fetchReferenceBytes(context.Background(), srv.URL+"/missing.png")
	if err == nil {
		t.Fatal("fetchReferenceBytes() error = nil, want HTTP 404")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("error = %v, want HTTP 404", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}
