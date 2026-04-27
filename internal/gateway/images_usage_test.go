package gateway

import "testing"

func TestImageCountFromSignedURLsFallsBackToRequestN(t *testing.T) {
	if got := imageCountFromSignedURLs([]string{"a", "b"}, 4); got != 2 {
		t.Fatalf("imageCountFromSignedURLs = %d, want 2", got)
	}
	if got := imageCountFromSignedURLs(nil, 3); got != 3 {
		t.Fatalf("imageCountFromSignedURLs nil = %d, want request count 3", got)
	}
	if got := imageCountFromSignedURLs(nil, 0); got != 1 {
		t.Fatalf("imageCountFromSignedURLs zero = %d, want 1", got)
	}
}
