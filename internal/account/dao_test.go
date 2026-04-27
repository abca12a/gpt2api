package account

import (
	"database/sql"
	"testing"
	"time"
)

func TestFillClearsStaleTodayUsedCount(t *testing.T) {
	previousDay := time.Now().AddDate(0, 0, -1)
	account := &Account{
		TodayUsedCount: 7,
		TodayUsedDate:  sql.NullTime{Time: previousDay, Valid: true},
	}

	fill(account)

	if account.TodayUsedCount != 0 {
		t.Fatalf("TodayUsedCount = %d, want stale count cleared", account.TodayUsedCount)
	}
}

func TestFillKeepsCurrentTodayUsedCount(t *testing.T) {
	account := &Account{
		TodayUsedCount: 3,
		TodayUsedDate:  sql.NullTime{Time: time.Now(), Valid: true},
	}

	fill(account)

	if account.TodayUsedCount != 3 {
		t.Fatalf("TodayUsedCount = %d, want current count preserved", account.TodayUsedCount)
	}
}
