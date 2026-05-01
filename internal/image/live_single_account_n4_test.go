package image

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"

	_ "github.com/go-sql-driver/mysql"

	"github.com/432539/gpt2api/internal/account"
	"github.com/432539/gpt2api/internal/config"
	"github.com/432539/gpt2api/internal/proxy"
	"github.com/432539/gpt2api/internal/scheduler"
	"github.com/432539/gpt2api/pkg/crypto"
	"github.com/432539/gpt2api/pkg/lock"
)

func TestLiveSingleAccountN4(t *testing.T) {
	if os.Getenv("GPT2API_LIVE_SINGLE_ACCOUNT_N4") != "1" {
		t.Skip("set GPT2API_LIVE_SINGLE_ACCOUNT_N4=1 to run live single-account n=4 probe")
	}
	dsn := os.Getenv("GPT2API_TEST_MYSQL_DSN")
	aesKey := os.Getenv("GPT2API_TEST_AES_KEY")
	if dsn == "" || aesKey == "" {
		t.Fatal("GPT2API_TEST_MYSQL_DSN and GPT2API_TEST_AES_KEY are required")
	}

	db, err := sqlx.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}

	cipher, err := crypto.NewAESGCM(aesKey)
	if err != nil {
		t.Fatal(err)
	}
	accSvc := account.NewService(account.NewDAO(db), cipher)
	proxySvc := proxy.NewService(proxy.NewDAO(db), cipher)
	redisAddr := os.Getenv("GPT2API_TEST_REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "127.0.0.1:6379"
	}
	redisClient := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer redisClient.Close()
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}

	if rawAccountID := os.Getenv("GPT2API_LIVE_ACCOUNT_ID"); rawAccountID != "" {
		accountID, err := strconv.ParseUint(rawAccountID, 10, 64)
		if err != nil || accountID == 0 {
			t.Fatalf("invalid GPT2API_LIVE_ACCOUNT_ID=%q", rawAccountID)
		}
		releaseLocks := reserveOtherFreeAccounts(t, db, redisClient, accountID)
		defer releaseLocks()
	}

	sched := scheduler.New(accSvc, proxySvc, lock.NewRedisLock(redisClient), config.SchedulerConfig{
		MinIntervalSec:   0,
		DailyUsageRatio:  1,
		LockTTLSec:       1200,
		Cooldown429Sec:   600,
		WarnedPauseHours: 24,
	})
	r := NewRunner(sched, nil)

	rounds := 1
	if raw := os.Getenv("GPT2API_LIVE_SINGLE_ACCOUNT_N4_ROUNDS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			t.Fatalf("invalid GPT2API_LIVE_SINGLE_ACCOUNT_N4_ROUNDS=%q", raw)
		}
		rounds = parsed
	}
	failures := 0
	for round := 1; round <= rounds; round++ {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		opt := RunOptions{
			UpstreamModel:     "gpt-5-3",
			Prompt:            "Live probe: create exactly four separate generated image attachments, not a collage and not one combined image. Use simple numbered square cards 1, 2, 3, and 4.",
			N:                 4,
			MaxAttempts:       1,
			DispatchTimeout:   10 * time.Second,
			PerAttemptTimeout: 4 * time.Minute,
			PollMaxWait:       120 * time.Second,
			PreferredPlanType: "free",
			RequirePlanType:   true,
		}
		result := runLiveSingleAccountN4Probe(ctx, r, opt)
		cancel()
		diag := liveSingleAccountN4Diagnostic(round, rounds, opt, result)
		diagJSON, err := json.Marshal(diag)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("GPT2API_IMAGE_N4_DIAGNOSTIC_JSON=%s", diagJSON)
		if result.Status != StatusSuccess || len(result.FileIDs) != 4 || len(result.SignedURLs) != 4 {
			failures++
		}
		if round < rounds {
			time.Sleep(3 * time.Second)
		}
	}
	if failures > 0 {
		t.Fatalf("single-account n=4 probe got incomplete result in %d/%d rounds", failures, rounds)
	}
}

type liveSingleAccountN4Output struct {
	Tool           string              `json:"tool"`
	Round          int                 `json:"round"`
	Rounds         int                 `json:"rounds"`
	Mode           string              `json:"mode"`
	Requested      int                 `json:"requested"`
	Status         string              `json:"status"`
	ErrorCode      string              `json:"error_code,omitempty"`
	ErrorMessage   string              `json:"error_message,omitempty"`
	DurationMs     int64               `json:"duration_ms"`
	Attempts       int                 `json:"attempts"`
	AccountID      uint64              `json:"account_id,omitempty"`
	PlanType       string              `json:"plan_type,omitempty"`
	Conversation   string              `json:"conversation_id,omitempty"`
	FileIDCount    int                 `json:"file_id_count"`
	SignedURLCount int                 `json:"signed_url_count"`
	FileIDs        []string            `json:"file_ids,omitempty"`
	Parts          []RunPartDiagnostic `json:"parts,omitempty"`
	Merge          *RunMergeDiagnostic `json:"merge,omitempty"`
	Diagnosis      string              `json:"diagnosis"`
	NextCheck      string              `json:"next_check"`
}

func runLiveSingleAccountN4Probe(ctx context.Context, r *Runner, opt RunOptions) *RunResult {
	start := time.Now()
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("GPT2API_LIVE_SINGLE_ACCOUNT_N4_MODE")))
	if mode == "parallel" || mode == "merge" || mode == "runner" {
		result := r.Run(ctx, opt)
		if result.DurationMs == 0 {
			result.DurationMs = time.Since(start).Milliseconds()
		}
		return result
	}

	result := &RunResult{Status: StatusFailed, ErrorCode: ErrUnknown}
	ok, status, err := r.runOnce(ctx, opt, result)
	if ok {
		result.Status = StatusSuccess
		result.ErrorCode = ""
		result.ErrorMessage = ""
	} else {
		result.ErrorCode = status
		if err != nil {
			result.ErrorMessage = err.Error()
		}
	}
	result.Attempts = 1
	result.DurationMs = time.Since(start).Milliseconds()
	return result
}

func liveSingleAccountN4Mode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("GPT2API_LIVE_SINGLE_ACCOUNT_N4_MODE")))
	switch mode {
	case "parallel", "merge", "runner":
		return "parallel_merge"
	default:
		return "single_run_once"
	}
}

func liveSingleAccountN4Diagnostic(round, rounds int, opt RunOptions, result *RunResult) liveSingleAccountN4Output {
	out := liveSingleAccountN4Output{
		Tool:           "gpt-image-2-single-account-n4",
		Round:          round,
		Rounds:         rounds,
		Mode:           liveSingleAccountN4Mode(),
		Requested:      opt.N,
		Status:         result.Status,
		ErrorCode:      result.ErrorCode,
		ErrorMessage:   result.ErrorMessage,
		DurationMs:     result.DurationMs,
		Attempts:       result.Attempts,
		AccountID:      result.AccountID,
		PlanType:       result.AccountPlanType,
		Conversation:   result.ConversationID,
		FileIDCount:    len(result.FileIDs),
		SignedURLCount: len(result.SignedURLs),
		FileIDs:        append([]string(nil), result.FileIDs...),
		Parts:          append([]RunPartDiagnostic(nil), result.Parts...),
		Merge:          result.Merge,
	}
	out.Diagnosis, out.NextCheck = diagnoseLiveSingleAccountN4(out)
	return out
}

func diagnoseLiveSingleAccountN4(out liveSingleAccountN4Output) (string, string) {
	if out.Mode == "single_run_once" {
		if out.Status == StatusSuccess && out.FileIDCount >= out.Requested && out.SignedURLCount >= out.Requested {
			return "single_account_run_once_complete", "再跑 parallel_merge 模式确认 Runner 合并与代理回源"
		}
		if out.Status == StatusSuccess && out.FileIDCount < out.Requested {
			return "single_account_upstream_returned_fewer_file_ids", "查看同一行 account_id/conversation_id/file_ids 和 runner SSE/poll 日志"
		}
		return "single_account_run_once_failed", "先处理 error_code/error_message 对应的账号或上游阶段"
	}
	if out.Merge != nil && out.Merge.Complete {
		return "parallel_merge_complete", "用任务查询 result.data[] 和 /p/img 逐张 HTTP 状态确认代理回源"
	}
	if out.Merge != nil && out.Merge.SucceededParts > 0 && !out.Merge.Complete {
		return "parallel_merge_partial_result", "检查 parts 中 failed part 的 first_failure/final_error；若 file_id_count 够但 result.data[] 少，再查落库/任务响应"
	}
	if out.Merge != nil && out.Merge.SucceededParts == 0 {
		return "parallel_all_parts_failed", "先处理 parts 的 first_failure；这还没进入结果合并或代理回源"
	}
	return "parallel_merge_unknown", "保留 JSON 与 runner 并发日志一起对照"
}

func reserveOtherFreeAccounts(t *testing.T, db *sqlx.DB, redisClient *redis.Client, targetID uint64) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var ids []uint64
	if err := db.SelectContext(ctx, &ids, `
SELECT id
FROM oai_accounts
WHERE deleted_at IS NULL
  AND status IN ('healthy', 'warned')
  AND LOWER(TRIM(plan_type)) = 'free'
  AND (cooldown_until IS NULL OR cooldown_until <= NOW())
  AND (token_expires_at IS NULL OR token_expires_at > NOW())
  AND id <> ?
ORDER BY id ASC`, targetID); err != nil {
		t.Fatal(err)
	}

	rl := lock.NewRedisLock(redisClient)
	token := fmt.Sprintf("live-single-account-n4-%d", time.Now().UnixNano())
	locked := make([]uint64, 0, len(ids))
	for _, id := range ids {
		key := fmt.Sprintf("acct:lock:%d", id)
		if err := rl.Acquire(ctx, key, token, 20*time.Minute); err != nil {
			if errors.Is(err, lock.ErrNotAcquired) {
				continue
			}
			t.Fatal(err)
		}
		locked = append(locked, id)
	}
	t.Logf("temporarily locked %d other free accounts to force account_id=%d", len(locked), targetID)

	return func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, id := range locked {
			_ = rl.Release(releaseCtx, fmt.Sprintf("acct:lock:%d", id), token)
		}
	}
}
