package account

import (
	"database/sql"
	"testing"
	"time"
)

func TestParseQuotaProbePayloadUsesReturnedMaxValue(t *testing.T) {
	payload := []byte(`{
		"default_model_slug":"gpt-5",
		"blocked_features":["x"],
		"limits_progress":[
			{"feature_name":"image_gen","remaining":4,"max_value":12,"reset_after":"2026-04-27T20:00:00Z"},
			{"feature_name":"text","remaining":99,"max_value":100}
		]
	}`)

	out, err := parseQuotaProbePayload(payload, &Account{}, time.Now())
	if err != nil {
		t.Fatalf("parseQuotaProbePayload: %v", err)
	}
	if out.remaining != 4 || out.total != 12 {
		t.Fatalf("quota = remaining %d total %d, want 4/12", out.remaining, out.total)
	}
	if out.defaultModel != "gpt-5" || len(out.blockedFeatures) != 1 || out.blockedFeatures[0] != "x" {
		t.Fatalf("metadata not parsed: %#v", out)
	}
	if out.resetAt.IsZero() {
		t.Fatal("resetAt was not parsed")
	}
}

func TestParseQuotaProbePayloadEstimatesTotalFromTodayUsage(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.Local)
	payload := []byte(`{"limits_progress":[{"feature_name":"image_generation","remaining":5}]}`)
	account := &Account{
		TodayUsedCount: 8,
		TodayUsedDate:  sql.NullTime{Time: now.Add(-2 * time.Hour), Valid: true},
	}

	out, err := parseQuotaProbePayload(payload, account, now)
	if err != nil {
		t.Fatalf("parseQuotaProbePayload: %v", err)
	}
	if out.remaining != 5 || out.total != 13 {
		t.Fatalf("quota = remaining %d total %d, want 5/13", out.remaining, out.total)
	}
}
