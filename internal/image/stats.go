package image

import (
	"sort"
	"strings"
	"time"
)

const traceStepStatusSkipped = "skipped"

type ProviderTraceStatRow struct {
	TaskID               string     `db:"task_id"`
	Status               string     `db:"status"`
	ProviderTrace        []byte     `db:"provider_trace"`
	Error                string     `db:"error"`
	CreatedAt            time.Time  `db:"created_at"`
	StartedAt            *time.Time `db:"started_at"`
	FinishedAt           *time.Time `db:"finished_at"`
	AccountID            uint64     `db:"account_id"`
	AccountEmail         string     `db:"account_email"`
	AccountPlanType      string     `db:"account_plan_type"`
	AccountStatus        string     `db:"account_status"`
	AccountCooldownUntil *time.Time `db:"account_cooldown_until"`
}

type ProviderHitStat struct {
	Provider      string `json:"provider"`
	DisplayName   string `json:"display_name"`
	Attempted     int    `json:"attempted"`
	Skipped       int    `json:"skipped"`
	FirstSelected int    `json:"first_selected"`
	FinalSelected int    `json:"final_selected"`
	Success       int    `json:"success"`
	Failed        int    `json:"failed"`
	FallbackFrom  int    `json:"fallback_from"`
}

type ProviderTransitionStat struct {
	FromProvider string `json:"from_provider"`
	ToProvider   string `json:"to_provider"`
	Display      string `json:"display"`
	Count        int    `json:"count"`
}

type ProviderTraceStats struct {
	WindowHours       int                      `json:"window_hours"`
	Total             int                      `json:"total"`
	Success           int                      `json:"success"`
	Failed            int                      `json:"failed"`
	FallbackTriggered int                      `json:"fallback_triggered"`
	Providers         []ProviderHitStat        `json:"providers"`
	Transitions       []ProviderTransitionStat `json:"transitions"`
	Slow              SlowTaskStats            `json:"slow"`
	Accounts          []AccountRealtimeStat    `json:"accounts"`
}

type AccountRealtimeStat struct {
	AccountID           uint64  `json:"account_id"`
	Email               string  `json:"email,omitempty"`
	PlanType            string  `json:"plan_type,omitempty"`
	Status              string  `json:"status,omitempty"`
	RecentTotal         int     `json:"recent_total"`
	Success             int     `json:"success"`
	Failed              int     `json:"failed"`
	SuccessRate         float64 `json:"success_rate"`
	AvgFirstPacketMs    int64   `json:"avg_first_packet_ms,omitempty"`
	AvgCompletionMs     int64   `json:"avg_completion_ms,omitempty"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	LastErrorType       string  `json:"last_error_type,omitempty"`
	LastTaskID          string  `json:"last_task_id,omitempty"`
	LastStatus          string  `json:"last_status,omitempty"`
	LastFinishedAt      int64   `json:"last_finished_at,omitempty"`
	CooldownUntil       int64   `json:"cooldown_until,omitempty"`
	CooldownRemainingMs int64   `json:"cooldown_remaining_ms,omitempty"`
	InCooldown          bool    `json:"in_cooldown"`
	CircuitOpen         bool    `json:"circuit_open"`
}

type ProviderTraceStatsOptions struct {
	WindowHours   int
	SlowThreshold time.Duration
	SlowLimit     int
	Now           time.Time
}

type SlowTaskStats struct {
	ThresholdMs int64               `json:"threshold_ms"`
	Total       int                 `json:"total"`
	Phases      []SlowTaskPhaseStat `json:"phases"`
	Tasks       []SlowTaskOverview  `json:"tasks"`
}

type SlowTaskPhaseStat struct {
	Phase string `json:"phase"`
	Count int    `json:"count"`
	AvgMs int64  `json:"avg_ms"`
	MaxMs int64  `json:"max_ms"`
}

type SlowTaskOverview struct {
	TaskID               string `json:"task_id"`
	Status               string `json:"status"`
	ErrorCode            string `json:"error_code,omitempty"`
	ProviderTraceSummary string `json:"provider_trace_summary,omitempty"`
	TotalMs              int64  `json:"total_ms"`
	QueueMs              int64  `json:"queue_ms,omitempty"`
	SubmitMs             int64  `json:"submit_ms,omitempty"`
	UpstreamWaitMs       int64  `json:"upstream_wait_ms,omitempty"`
	PollMs               int64  `json:"poll_ms,omitempty"`
	DownloadMs           int64  `json:"download_ms,omitempty"`
	DominantPhase        string `json:"dominant_phase,omitempty"`
	DominantMs           int64  `json:"dominant_ms,omitempty"`
	CreatedAt            int64  `json:"created_at"`
	StartedAt            int64  `json:"started_at,omitempty"`
	FinishedAt           int64  `json:"finished_at,omitempty"`
}

func BuildProviderTraceStats(rows []ProviderTraceStatRow, windowHours int) ProviderTraceStats {
	return BuildProviderTraceStatsWithOptions(rows, ProviderTraceStatsOptions{
		WindowHours:   windowHours,
		SlowThreshold: DefaultSlowTaskThreshold(),
		SlowLimit:     10,
		Now:           time.Now(),
	})
}

func BuildProviderTraceStatsWithOptions(rows []ProviderTraceStatRow, opt ProviderTraceStatsOptions) ProviderTraceStats {
	if opt.WindowHours <= 0 {
		opt.WindowHours = 24
	}
	if opt.SlowThreshold <= 0 {
		opt.SlowThreshold = DefaultSlowTaskThreshold()
	}
	if opt.SlowLimit <= 0 {
		opt.SlowLimit = 10
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	stats := ProviderTraceStats{
		WindowHours: opt.WindowHours,
	}
	providerMap := make(map[string]*ProviderHitStat)
	transitionMap := make(map[string]*ProviderTransitionStat)

	for _, row := range rows {
		stats.Total++
		switch row.Status {
		case StatusSuccess:
			stats.Success++
		case StatusFailed:
			stats.Failed++
		}

		trace := DecodeProviderTrace(row.ProviderTrace)
		if trace == nil {
			continue
		}

		if first := firstAttemptedProvider(trace); first != "" {
			bucket := ensureProviderHitStat(providerMap, first)
			bucket.FirstSelected++
		}

		finalProvider := normalizeProviderKey(trace.Final.Provider)
		if finalProvider != "" {
			bucket := ensureProviderHitStat(providerMap, finalProvider)
			bucket.FinalSelected++
			switch row.Status {
			case StatusSuccess:
				bucket.Success++
			case StatusFailed:
				bucket.Failed++
			}
		}

		attemptedSeen := make(map[string]struct{})
		skippedSeen := make(map[string]struct{})
		for _, step := range trace.Steps {
			provider := normalizeProviderKey(step.Provider)
			if provider == "" {
				continue
			}
			bucket := ensureProviderHitStat(providerMap, provider)
			if step.Status == traceStepStatusSkipped {
				if _, ok := skippedSeen[provider]; !ok {
					bucket.Skipped++
					skippedSeen[provider] = struct{}{}
				}
				continue
			}
			if _, ok := attemptedSeen[provider]; ok {
				continue
			}
			attemptedSeen[provider] = struct{}{}
			bucket.Attempted++
		}

		if trace.Fallback != nil && trace.Fallback.Triggered {
			stats.FallbackTriggered++
			fromProvider := normalizeProviderKey(trace.Fallback.FromProvider)
			if fromProvider != "" {
				ensureProviderHitStat(providerMap, fromProvider).FallbackFrom++
			}
			if fromProvider != "" && finalProvider != "" {
				key := fromProvider + "->" + finalProvider
				transition := transitionMap[key]
				if transition == nil {
					transition = &ProviderTransitionStat{
						FromProvider: fromProvider,
						ToProvider:   finalProvider,
						Display:      traceProviderDisplayName(fromProvider) + " -> " + traceProviderDisplayName(finalProvider),
					}
					transitionMap[key] = transition
				}
				transition.Count++
			}
		}
	}

	stats.Providers = flattenProviderHitStats(providerMap)
	stats.Transitions = flattenProviderTransitions(transitionMap)
	stats.Slow = buildSlowTaskStats(rows, opt)
	stats.Accounts = buildAccountRealtimeStats(rows, opt.Now)
	return stats
}

type accountRealtimeAccumulator struct {
	stat             AccountRealtimeStat
	firstPacketTotal int64
	firstPacketCount int64
	completionTotal  int64
	completionCount  int64
	recentRows       []ProviderTraceStatRow
}

func buildAccountRealtimeStats(rows []ProviderTraceStatRow, now time.Time) []AccountRealtimeStat {
	if now.IsZero() {
		now = time.Now()
	}
	buckets := make(map[uint64]*accountRealtimeAccumulator)
	for _, row := range rows {
		accountID := accountIDFromStatRow(row)
		if accountID == 0 {
			continue
		}
		bucket := buckets[accountID]
		if bucket == nil {
			bucket = &accountRealtimeAccumulator{stat: AccountRealtimeStat{AccountID: accountID}}
			buckets[accountID] = bucket
		}
		bucket.recentRows = append(bucket.recentRows, row)
		mergeAccountIdentity(&bucket.stat, row, now)
		bucket.stat.RecentTotal++
		switch row.Status {
		case StatusSuccess:
			bucket.stat.Success++
		case StatusFailed:
			bucket.stat.Failed++
		}

		timing := TaskTimingBreakdownFromTask(&Task{
			TaskID:        row.TaskID,
			Status:        row.Status,
			ProviderTrace: row.ProviderTrace,
			Error:         row.Error,
			CreatedAt:     row.CreatedAt,
			StartedAt:     row.StartedAt,
			FinishedAt:    row.FinishedAt,
		}, now)
		if firstPacketMs := timing.SubmitMs + timing.UpstreamWaitMs; firstPacketMs > 0 {
			bucket.firstPacketTotal += firstPacketMs
			bucket.firstPacketCount++
		}
		if timing.TotalMs > 0 {
			bucket.completionTotal += timing.TotalMs
			bucket.completionCount++
		}
	}

	out := make([]AccountRealtimeStat, 0, len(buckets))
	for _, bucket := range buckets {
		stat := bucket.stat
		if stat.RecentTotal > 0 {
			stat.SuccessRate = roundPercent(float64(stat.Success) * 100 / float64(stat.RecentTotal))
		}
		if bucket.firstPacketCount > 0 {
			stat.AvgFirstPacketMs = bucket.firstPacketTotal / bucket.firstPacketCount
		}
		if bucket.completionCount > 0 {
			stat.AvgCompletionMs = bucket.completionTotal / bucket.completionCount
		}
		stat.ConsecutiveFailures, stat.LastErrorType, stat.LastTaskID, stat.LastStatus, stat.LastFinishedAt = accountConsecutiveFailures(bucket.recentRows)
		out = append(out, stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].InCooldown != out[j].InCooldown {
			return out[i].InCooldown
		}
		if out[i].ConsecutiveFailures != out[j].ConsecutiveFailures {
			return out[i].ConsecutiveFailures > out[j].ConsecutiveFailures
		}
		if out[i].AvgCompletionMs != out[j].AvgCompletionMs {
			return out[i].AvgCompletionMs > out[j].AvgCompletionMs
		}
		return out[i].AccountID < out[j].AccountID
	})
	return out
}

func accountIDFromStatRow(row ProviderTraceStatRow) uint64 {
	if row.AccountID > 0 {
		return row.AccountID
	}
	trace := DecodeProviderTrace(row.ProviderTrace)
	if trace == nil {
		return 0
	}
	if trace.Final.AccountID > 0 {
		return trace.Final.AccountID
	}
	for i := len(trace.Steps) - 1; i >= 0; i-- {
		if trace.Steps[i].AccountID > 0 {
			return trace.Steps[i].AccountID
		}
	}
	return trace.Original.AccountID
}

func mergeAccountIdentity(stat *AccountRealtimeStat, row ProviderTraceStatRow, now time.Time) {
	if stat.Email == "" {
		stat.Email = strings.TrimSpace(row.AccountEmail)
	}
	if stat.PlanType == "" {
		stat.PlanType = strings.TrimSpace(row.AccountPlanType)
	}
	if stat.Status == "" {
		stat.Status = strings.TrimSpace(row.AccountStatus)
	}
	if isAccountCircuitStatus(stat.Status) {
		stat.CircuitOpen = true
	}
	if row.AccountCooldownUntil == nil || row.AccountCooldownUntil.IsZero() {
		return
	}
	if stat.CooldownUntil == 0 || row.AccountCooldownUntil.Unix() > stat.CooldownUntil {
		stat.CooldownUntil = row.AccountCooldownUntil.Unix()
		remaining := row.AccountCooldownUntil.Sub(now)
		if remaining > 0 {
			stat.InCooldown = true
			stat.CircuitOpen = true
			stat.CooldownRemainingMs = remaining.Milliseconds()
		}
	}
}

func isAccountCircuitStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "throttled", "suspicious", "dead":
		return true
	default:
		return false
	}
}

func accountConsecutiveFailures(rows []ProviderTraceStatRow) (int, string, string, string, int64) {
	if len(rows) == 0 {
		return 0, "", "", "", 0
	}
	sort.Slice(rows, func(i, j int) bool {
		return statRowEventUnix(rows[i]) > statRowEventUnix(rows[j])
	})
	last := rows[0]
	lastFinishedAt := statRowEventUnix(last)
	lastErrorType := ""
	count := 0
	for idx, row := range rows {
		if row.Status != StatusFailed {
			break
		}
		if idx == 0 {
			lastErrorType = firstTaskErrorCode(row.Error)
			if lastErrorType == "" {
				lastErrorType = lastTraceReasonCode(row.ProviderTrace)
			}
		}
		count++
	}
	return count, lastErrorType, last.TaskID, last.Status, lastFinishedAt
}

func statRowEventUnix(row ProviderTraceStatRow) int64 {
	if row.FinishedAt != nil && !row.FinishedAt.IsZero() {
		return row.FinishedAt.Unix()
	}
	if row.StartedAt != nil && !row.StartedAt.IsZero() {
		return row.StartedAt.Unix()
	}
	return row.CreatedAt.Unix()
}

func lastTraceReasonCode(raw []byte) string {
	trace := DecodeProviderTrace(raw)
	if trace == nil {
		return ""
	}
	for i := len(trace.Steps) - 1; i >= 0; i-- {
		if code := strings.TrimSpace(trace.Steps[i].ReasonCode); code != "" {
			return code
		}
	}
	return ""
}

func roundPercent(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

func buildSlowTaskStats(rows []ProviderTraceStatRow, opt ProviderTraceStatsOptions) SlowTaskStats {
	stats := SlowTaskStats{
		ThresholdMs: opt.SlowThreshold.Milliseconds(),
	}
	phaseMap := make(map[string]*SlowTaskPhaseStat)
	tasks := make([]SlowTaskOverview, 0, opt.SlowLimit)

	for _, row := range rows {
		task := &Task{
			TaskID:        row.TaskID,
			Status:        row.Status,
			ProviderTrace: row.ProviderTrace,
			Error:         row.Error,
			CreatedAt:     row.CreatedAt,
			StartedAt:     row.StartedAt,
			FinishedAt:    row.FinishedAt,
		}
		timing := TaskTimingBreakdownFromTask(task, opt.Now)
		if timing.TotalMs < opt.SlowThreshold.Milliseconds() {
			continue
		}
		stats.Total++
		phase := timing.DominantPhase
		if phase == "" {
			phase = TaskPhaseUnknown
		}
		bucket := phaseMap[phase]
		if bucket == nil {
			bucket = &SlowTaskPhaseStat{Phase: phase}
			phaseMap[phase] = bucket
		}
		bucket.Count++
		bucket.AvgMs += timing.DominantMs
		if timing.DominantMs > bucket.MaxMs {
			bucket.MaxMs = timing.DominantMs
		}

		item := SlowTaskOverview{
			TaskID:               row.TaskID,
			Status:               row.Status,
			ErrorCode:            firstTaskErrorCode(row.Error),
			ProviderTraceSummary: TaskTraceSummary(task.DecodeProviderTrace()),
			TotalMs:              timing.TotalMs,
			QueueMs:              timing.QueueMs,
			SubmitMs:             timing.SubmitMs,
			UpstreamWaitMs:       timing.UpstreamWaitMs,
			PollMs:               timing.PollMs,
			DownloadMs:           timing.DownloadMs,
			DominantPhase:        phase,
			DominantMs:           timing.DominantMs,
			CreatedAt:            row.CreatedAt.Unix(),
		}
		if row.StartedAt != nil && !row.StartedAt.IsZero() {
			item.StartedAt = row.StartedAt.Unix()
		}
		if row.FinishedAt != nil && !row.FinishedAt.IsZero() {
			item.FinishedAt = row.FinishedAt.Unix()
		}
		tasks = append(tasks, item)
	}

	for _, bucket := range phaseMap {
		if bucket.Count > 0 {
			bucket.AvgMs = bucket.AvgMs / int64(bucket.Count)
		}
	}
	stats.Phases = flattenSlowTaskPhases(phaseMap)
	sort.Slice(tasks, func(i, j int) bool {
		leftTs := slowTaskSortUnix(tasks[i])
		rightTs := slowTaskSortUnix(tasks[j])
		if leftTs != rightTs {
			return leftTs > rightTs
		}
		if tasks[i].TotalMs != tasks[j].TotalMs {
			return tasks[i].TotalMs > tasks[j].TotalMs
		}
		return tasks[i].TaskID < tasks[j].TaskID
	})
	if len(tasks) > opt.SlowLimit {
		tasks = tasks[:opt.SlowLimit]
	}
	stats.Tasks = tasks
	return stats
}

func flattenSlowTaskPhases(stats map[string]*SlowTaskPhaseStat) []SlowTaskPhaseStat {
	if len(stats) == 0 {
		return nil
	}
	out := make([]SlowTaskPhaseStat, 0, len(stats))
	for _, stat := range stats {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].MaxMs != out[j].MaxMs {
			return out[i].MaxMs > out[j].MaxMs
		}
		return out[i].Phase < out[j].Phase
	})
	return out
}

func firstTaskErrorCode(stored string) string {
	if strings.TrimSpace(stored) == "" {
		return ""
	}
	code, _, _ := TaskErrorFields(stored)
	return code
}

func slowTaskSortUnix(task SlowTaskOverview) int64 {
	if task.FinishedAt > 0 {
		return task.FinishedAt
	}
	if task.StartedAt > 0 {
		return task.StartedAt
	}
	return task.CreatedAt
}

func ensureProviderHitStat(stats map[string]*ProviderHitStat, provider string) *ProviderHitStat {
	provider = normalizeProviderKey(provider)
	if provider == "" {
		return nil
	}
	if stat := stats[provider]; stat != nil {
		return stat
	}
	stat := &ProviderHitStat{
		Provider:    provider,
		DisplayName: traceProviderDisplayName(provider),
	}
	stats[provider] = stat
	return stat
}

func flattenProviderHitStats(stats map[string]*ProviderHitStat) []ProviderHitStat {
	if len(stats) == 0 {
		return nil
	}
	out := make([]ProviderHitStat, 0, len(stats))
	for _, stat := range stats {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		left := providerSortRank(out[i].Provider)
		right := providerSortRank(out[j].Provider)
		if left != right {
			return left < right
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

func flattenProviderTransitions(stats map[string]*ProviderTransitionStat) []ProviderTransitionStat {
	if len(stats) == 0 {
		return nil
	}
	out := make([]ProviderTransitionStat, 0, len(stats))
	for _, stat := range stats {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].FromProvider != out[j].FromProvider {
			return out[i].FromProvider < out[j].FromProvider
		}
		return out[i].ToProvider < out[j].ToProvider
	})
	return out
}

func firstAttemptedProvider(trace *TaskTrace) string {
	if trace == nil {
		return ""
	}
	for _, step := range trace.Steps {
		if step.Status == traceStepStatusSkipped {
			continue
		}
		if provider := normalizeProviderKey(step.Provider); provider != "" {
			return provider
		}
	}
	return normalizeProviderKey(trace.Original.Provider)
}

func normalizeProviderKey(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func providerSortRank(provider string) int {
	switch normalizeProviderKey(provider) {
	case TraceProviderCodex:
		return 0
	case TraceProviderAPIMart:
		return 1
	case TraceProviderFreeRunner:
		return 2
	case TraceProviderAccountRunner:
		return 3
	case TraceProviderOpenAI:
		return 4
	case TraceProviderGemini:
		return 5
	default:
		return 99
	}
}
