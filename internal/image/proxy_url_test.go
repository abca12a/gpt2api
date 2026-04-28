package image

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildTaskImageURLsPrefersProxyWhenFileIDsExist(t *testing.T) {
	fileIDs, err := json.Marshal([]string{"sed:file-a", "sed:file-b"})
	if err != nil {
		t.Fatal(err)
	}
	resultURLs, err := json.Marshal([]string{"https://upstream.example/a.png", "https://upstream.example/b.png"})
	if err != nil {
		t.Fatal(err)
	}
	task := &Task{
		TaskID:     "task-123",
		FileIDs:    fileIDs,
		ResultURLs: resultURLs,
	}

	urls := BuildTaskImageURLs(task, time.Hour)
	if len(urls) != 2 {
		t.Fatalf("expected 2 urls, got %d", len(urls))
	}
	for i, url := range urls {
		wantPrefix := "/p/img/task-123/"
		if !strings.HasPrefix(url, wantPrefix) {
			t.Fatalf("url %d should use proxy prefix %q, got %q", i, wantPrefix, url)
		}
		if strings.Contains(url, "upstream.example") {
			t.Fatalf("url %d leaked upstream url: %q", i, url)
		}
	}
}

func TestBuildTaskImageURLsFallsBackToLegacyResultURLs(t *testing.T) {
	resultURLs, err := json.Marshal([]string{"https://upstream.example/a.png"})
	if err != nil {
		t.Fatal(err)
	}
	task := &Task{
		TaskID:     "legacy-task",
		ResultURLs: resultURLs,
	}

	urls := BuildTaskImageURLs(task, time.Hour)
	if len(urls) != 1 || urls[0] != "https://upstream.example/a.png" {
		t.Fatalf("expected legacy upstream fallback, got %#v", urls)
	}
}

func TestBuildTaskImageURLsProxiesInlineDataURLsWithoutFileIDs(t *testing.T) {
	resultURLs, err := json.Marshal([]string{"data:image/png;base64,aGVsbG8="})
	if err != nil {
		t.Fatal(err)
	}
	task := &Task{
		TaskID:     "inline-task",
		ResultURLs: resultURLs,
	}

	urls := BuildTaskImageURLs(task, time.Hour)
	if len(urls) != 1 {
		t.Fatalf("expected 1 url, got %d", len(urls))
	}
	if !strings.HasPrefix(urls[0], "/p/img/inline-task/0") {
		t.Fatalf("inline data url should use proxy, got %q", urls[0])
	}
	if strings.Contains(urls[0], "data:image") {
		t.Fatalf("inline data url leaked to client: %q", urls[0])
	}
}
