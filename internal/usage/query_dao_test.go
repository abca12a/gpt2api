package usage

import (
	"strings"
	"testing"
)

func TestImageCountForStatsExpressionBackfillsLegacySuccessRows(t *testing.T) {
	expr := imageCountForStatsExpr()
	for _, fragment := range []string{"u.type='image'", "u.status='success'", "GREATEST(u.image_count, 1)"} {
		if !strings.Contains(expr, fragment) {
			t.Fatalf("image count expression missing %q: %s", fragment, expr)
		}
	}
}
