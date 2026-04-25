package chatgpt

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseSSEReadTimeout(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	out := make(chan SSEEvent, 1)
	go parseSSE(pr, out, 20*time.Millisecond)

	select {
	case ev, ok := <-out:
		if !ok {
			t.Fatal("expected timeout event, got closed channel")
		}
		if ev.Err == nil || !strings.Contains(ev.Err.Error(), "sse read timeout") {
			t.Fatalf("expected timeout error, got %#v", ev.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("parseSSE did not time out")
	}

	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("expected channel to close after timeout")
		}
	case <-time.After(time.Second):
		t.Fatal("parseSSE did not close channel after timeout")
	}
}

func TestParseSSEParsesEventBeforeTimeout(t *testing.T) {
	body := io.NopCloser(strings.NewReader("event: message\ndata: {\"ok\":true}\n\n"))
	out := make(chan SSEEvent, 1)
	go parseSSE(body, out, time.Second)

	ev, ok := <-out
	if !ok {
		t.Fatal("expected event, got closed channel")
	}
	if ev.Err != nil || ev.Event != "message" || string(ev.Data) != `{"ok":true}` {
		t.Fatalf("unexpected event: %#v", ev)
	}
}

func TestParseImageSSERecordsStreamError(t *testing.T) {
	stream := make(chan SSEEvent, 1)
	stream <- SSEEvent{Err: io.ErrUnexpectedEOF}
	close(stream)

	res := ParseImageSSE(stream)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "unexpected EOF") {
		t.Fatalf("expected stream error, got %#v", res.Err)
	}
}
