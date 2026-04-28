package image

import "testing"

func TestDecodeInlineImageDataURL(t *testing.T) {
	data, contentType, err := DecodeInlineImageDataURL("data:image/png;base64,aGVsbG8=")
	if err != nil {
		t.Fatalf("DecodeInlineImageDataURL error: %v", err)
	}
	if contentType != "image/png" {
		t.Fatalf("contentType = %q, want image/png", contentType)
	}
	if string(data) != "hello" {
		t.Fatalf("data = %q, want hello", string(data))
	}
}

func TestDecodeInlineImageDataURLRejectsNonImage(t *testing.T) {
	if _, _, err := DecodeInlineImageDataURL("data:text/plain;base64,aGVsbG8="); err == nil {
		t.Fatal("expected error for non-image data url")
	}
}
