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

func TestParseImageSSECapturesAssistantText(t *testing.T) {
	stream := make(chan SSEEvent, 2)
	stream <- SSEEvent{Data: []byte(`{"v":{"conversation_id":"conv_1","message":{"author":{"role":"assistant"},"content":{"content_type":"text","parts":["I can't help create that image because it may violate safety policy."]},"metadata":{"finish_details":{"type":"stop"}}}}}`)}
	stream <- SSEEvent{Data: []byte(`[DONE]`)}
	close(stream)

	res := ParseImageSSE(stream)
	if res.ConversationID != "conv_1" || res.FinishType != "stop" {
		t.Fatalf("unexpected sse metadata: %#v", res)
	}
	if !strings.Contains(res.AssistantText, "safety policy") {
		t.Fatalf("assistant text not captured: %#v", res.AssistantText)
	}
}

func TestExtractAssistantTextsFromConversationMapping(t *testing.T) {
	mapping := map[string]interface{}{
		"old": map[string]interface{}{
			"message": map[string]interface{}{
				"author":      map[string]interface{}{"role": "assistant"},
				"create_time": float64(1),
				"content":     map[string]interface{}{"content_type": "text", "parts": []interface{}{"older reason"}},
			},
		},
		"new": map[string]interface{}{
			"message": map[string]interface{}{
				"author":      map[string]interface{}{"role": "assistant"},
				"create_time": float64(2),
				"content":     map[string]interface{}{"content_type": "text", "parts": []interface{}{"latest refusal reason"}},
			},
		},
		"user": map[string]interface{}{
			"message": map[string]interface{}{
				"author":      map[string]interface{}{"role": "user"},
				"create_time": float64(3),
				"content":     map[string]interface{}{"content_type": "text", "parts": []interface{}{"original prompt should be ignored"}},
			},
		},
	}

	texts := ExtractAssistantTexts(mapping)
	if len(texts) != 2 {
		t.Fatalf("assistant text count = %d, want 2: %#v", len(texts), texts)
	}
	if got := LatestAssistantText(mapping); got != "latest refusal reason" {
		t.Fatalf("LatestAssistantText = %q", got)
	}
}
