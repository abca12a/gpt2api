package codexusage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotCountsTodayImageUsageByAuthEmail(t *testing.T) {
	tmp := t.TempDir()
	authDir := filepath.Join(tmp, "auths")
	if err := os.Mkdir(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(authDir, "codex-alice@example.com-plus.json"), `{"email":"alice@example.com","disabled":false}`)
	writeFile(t, filepath.Join(authDir, "codex-bob@example.com-team.json"), `{"email":"bob@example.com","disabled":true}`)

	logPath := filepath.Join(tmp, "main.log")
	writeFile(t, logPath, `
[2026-04-26 23:59:59] [oldreq] [debug] [conductor.go:3518] Use OAuth provider=codex auth_file=codex-alice@example.com-plus.json for model gpt-5.4-mini
[2026-04-26 23:59:59] [oldreq] [info ] [gin_logger.go:94] 200 |          1m0s |      172.18.0.4 | POST    "/v1/images/generations"
[2026-04-27 10:00:00] [reqok] [debug] [conductor.go:3518] Use OAuth provider=codex auth_file=codex-alice@example.com-plus.json for model gpt-5.4-mini
[2026-04-27 10:01:00] [reqok] [info ] [gin_logger.go:94] 200 |          1m0s |      172.18.0.4 | POST    "/v1/images/generations"
[2026-04-27 10:02:00] [reqfail] [debug] [conductor.go:3518] Use OAuth provider=codex auth_file=codex-alice@example.com-plus.json for model gpt-5.4-mini
[2026-04-27 10:02:05] [reqfail] [debug] [codex_executor.go:468] request error, error status: 429, error message: The usage limit has been reached
[2026-04-27 10:02:06] [reqfail] [warn ] [gin_logger.go:92] 408 |          1m59s |      172.18.0.4 | POST    "/v1/images/edits"
[2026-04-27 10:03:00] [chatreq] [debug] [conductor.go:3518] Use OAuth provider=codex auth_file=codex-alice@example.com-plus.json for model gpt-5.4-mini
[2026-04-27 10:03:10] [chatreq] [info ] [gin_logger.go:94] 200 |          1.2s |      172.18.0.4 | POST    "/v1/chat/completions"
`)

	svc := New(Options{
		AuthDir: authDir,
		LogPath: logPath,
		Now: func() time.Time {
			return time.Date(2026, 4, 27, 18, 0, 0, 0, time.Local)
		},
	})
	snap, err := svc.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if !snap.StatsAvailable {
		t.Fatal("expected stats to be available")
	}
	if got := snap.Summary.ActiveAccounts; got != 1 {
		t.Fatalf("active accounts = %d, want 1", got)
	}
	if got := snap.Summary.DisabledAccounts; got != 1 {
		t.Fatalf("disabled accounts = %d, want 1", got)
	}
	alice := snap.ByEmail["alice@example.com"]
	if alice == nil {
		t.Fatal("missing alice usage")
	}
	if !alice.ExternalPool || alice.ExternalDisabled || alice.ExternalPlan != "plus" {
		t.Fatalf("unexpected alice pool metadata: %#v", alice)
	}
	if alice.RequestsToday != 2 || alice.SuccessToday != 1 || alice.FailedToday != 1 || alice.Quota429Today != 1 {
		t.Fatalf("unexpected alice counters: %#v", alice)
	}
	if snap.Summary.RequestsToday != 2 || snap.Summary.SuccessToday != 1 || snap.Summary.FailedToday != 1 || snap.Summary.Quota429EventsToday != 1 || snap.Summary.Quota429AccountsToday != 1 {
		t.Fatalf("unexpected summary: %#v", snap.Summary)
	}
	bob := snap.ByEmail["bob@example.com"]
	if bob == nil || !bob.ExternalPool || !bob.ExternalDisabled || bob.ExternalPlan != "team" {
		t.Fatalf("unexpected bob usage: %#v", bob)
	}
}

func TestSnapshotFallsBackToEmailFromFilename(t *testing.T) {
	tmp := t.TempDir()
	authDir := filepath.Join(tmp, "auths")
	if err := os.Mkdir(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(authDir, "codex-name-with-dash@example.com-plus.json"), `{"disabled":false}`)
	logPath := filepath.Join(tmp, "main.log")
	writeFile(t, logPath, `[2026-04-27 11:00:00] [req] [debug] [conductor.go:3518] Use OAuth provider=codex auth_file=codex-name-with-dash@example.com-plus.json for model gpt-5.4-mini
[2026-04-27 11:00:30] [req] [info ] [gin_logger.go:94] 200 |          30s |      172.18.0.4 | POST    "/v1/images/generations"
`)

	svc := New(Options{
		AuthDir: authDir,
		LogPath: logPath,
		Now: func() time.Time {
			return time.Date(2026, 4, 27, 12, 0, 0, 0, time.Local)
		},
	})
	snap, err := svc.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	usage := snap.ByEmail["name-with-dash@example.com"]
	if usage == nil || usage.SuccessToday != 1 || usage.ExternalPlan != "plus" {
		t.Fatalf("unexpected usage from filename: %#v", usage)
	}
}

func writeFile(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
