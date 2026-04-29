package image

import (
	"sort"
	"strings"
)

const traceStepStatusSkipped = "skipped"

type ProviderTraceStatRow struct {
	Status        string `db:"status"`
	ProviderTrace []byte `db:"provider_trace"`
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
}

func BuildProviderTraceStats(rows []ProviderTraceStatRow, windowHours int) ProviderTraceStats {
	stats := ProviderTraceStats{
		WindowHours: windowHours,
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
	return stats
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
