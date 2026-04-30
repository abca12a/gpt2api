package gateway

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/channel"
	imagepkg "github.com/432539/gpt2api/internal/image"
	"github.com/432539/gpt2api/pkg/logger"
)

const (
	imageFallbackStatusSkipped              = "skipped"
	imageFallbackReasonSkipCodex            = "policy_skip_codex"
	imageFallbackReasonChannelWarmup        = "channel_cooldown"
	imageFallbackReasonResolutionRunnerOnly = "resolution_runner_only"
)

type imageFallbackPolicy struct {
	ChannelOrder         []string
	RunnerPlans          []imageRunnerFallbackPlan
	ChannelCooldown      time.Duration
	ChannelFailThreshold int
	SkipCodexToAPIMart   bool
	DisableChannels      bool
	DisableChannelReason string
}

type imageRunnerFallbackPlan struct {
	Token             string
	PreferredPlanType string
	RequirePlanType   bool
}

func defaultImageFallbackPolicy() imageFallbackPolicy {
	return imageFallbackPolicy{
		ChannelOrder:         []string{"codex", "apimart"},
		RunnerPlans:          []imageRunnerFallbackPlan{{Token: "free", PreferredPlanType: "free", RequirePlanType: true}},
		ChannelCooldown:      5 * time.Minute,
		ChannelFailThreshold: 3,
	}
}

func (h *Handler) imageFallbackPolicy() imageFallbackPolicy {
	policy := defaultImageFallbackPolicy()
	if h == nil || h.Settings == nil {
		return policy
	}
	if order := normalizeImageFallbackTokens(h.Settings.ImageChannelFallbackOrder()); len(order) > 0 {
		policy.ChannelOrder = order
	}
	if rawPlans := h.Settings.ImageAccountFallbackOrder(); len(rawPlans) > 0 {
		policy.RunnerPlans = parseImageRunnerFallbackPlans(rawPlans)
	}
	if cooldown := h.Settings.ImageChannelCooldownSec(); cooldown > 0 {
		policy.ChannelCooldown = time.Duration(cooldown) * time.Second
	} else if cooldown == 0 {
		policy.ChannelCooldown = 0
	}
	if threshold := h.Settings.ImageChannelFailThreshold(); threshold >= 0 {
		policy.ChannelFailThreshold = threshold
	}
	policy.SkipCodexToAPIMart = h.Settings.ImageSkipCodexToAPIMart()
	return policy
}

func imageFallbackPolicyForResolution(policy imageFallbackPolicy, resolution string) imageFallbackPolicy {
	switch normalizeImageResolutionToken(resolution) {
	case "2k", "4k":
		policy.ChannelOrder = []string{imagepkg.TraceProviderCodex, imagepkg.TraceProviderAPIMart}
		if len(policy.RunnerPlans) == 0 {
			policy.RunnerPlans = defaultImageFallbackPolicy().RunnerPlans
		}
		policy.DisableChannels = false
		policy.DisableChannelReason = ""
		return policy
	default:
		policy.ChannelOrder = nil
		policy.RunnerPlans = defaultImageFallbackPolicy().RunnerPlans
		policy.DisableChannels = true
		policy.DisableChannelReason = imageFallbackReasonResolutionRunnerOnly
		return policy
	}
}

func normalizeImageFallbackTokens(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, token := range raw {
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseImageRunnerFallbackPlans(raw []string) []imageRunnerFallbackPlan {
	raw = normalizeImageFallbackTokens(raw)
	if len(raw) == 0 {
		return defaultImageFallbackPolicy().RunnerPlans
	}
	plans := make([]imageRunnerFallbackPlan, 0, len(raw))
	for _, token := range raw {
		switch token {
		case "none", "off", "disable", "disabled":
			return nil
		case "any", "runner", "account_runner":
			plans = append(plans, imageRunnerFallbackPlan{Token: token})
		default:
			token = strings.TrimPrefix(token, "plan:")
			plans = append(plans, imageRunnerFallbackPlan{
				Token:             token,
				PreferredPlanType: token,
				RequirePlanType:   token != "",
			})
		}
	}
	return dedupeImageRunnerFallbackPlans(plans)
}

func dedupeImageRunnerFallbackPlans(plans []imageRunnerFallbackPlan) []imageRunnerFallbackPlan {
	if len(plans) == 0 {
		return nil
	}
	out := make([]imageRunnerFallbackPlan, 0, len(plans))
	seen := make(map[string]struct{}, len(plans))
	for _, plan := range plans {
		key := strings.ToLower(strings.TrimSpace(plan.Token))
		if key == "" && !plan.RequirePlanType && strings.TrimSpace(plan.PreferredPlanType) == "" {
			key = "any"
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, plan)
	}
	return out
}

func cloneImageRunnerFallbackPlans(plans []imageRunnerFallbackPlan) []imageRunnerFallbackPlan {
	if len(plans) == 0 {
		return nil
	}
	out := make([]imageRunnerFallbackPlan, len(plans))
	copy(out, plans)
	return out
}

func isFreeRunnerPlan(plan imageRunnerFallbackPlan) bool {
	return plan.RequirePlanType && strings.EqualFold(strings.TrimSpace(plan.PreferredPlanType), "free")
}

func firstImageRunnerFallbackPlan(plans []imageRunnerFallbackPlan) imageRunnerFallbackPlan {
	if len(plans) == 0 {
		return imageRunnerFallbackPlan{}
	}
	return plans[0]
}

func applyImageRunnerFallbackPlan(opt *imagepkg.RunOptions, plan imageRunnerFallbackPlan) {
	if opt == nil {
		return
	}
	opt.PreferredPlanType = strings.TrimSpace(plan.PreferredPlanType)
	opt.RequirePlanType = plan.RequirePlanType
}

func shouldContinueImageRunnerFallback(code string) bool {
	switch strings.TrimSpace(code) {
	case "", imagepkg.ErrContentModeration:
		return false
	default:
		return true
	}
}

func (h *ImagesHandler) runImageWithFallbackPlans(ctx context.Context, base imagepkg.RunOptions, plans []imageRunnerFallbackPlan) (*imagepkg.RunResult, imageRunnerFallbackPlan) {
	if h == nil || h.Runner == nil {
		return &imagepkg.RunResult{Status: imagepkg.StatusFailed, ErrorCode: imagepkg.ErrUnknown, ErrorMessage: "image runner not configured"}, imageRunnerFallbackPlan{}
	}
	plans = cloneImageRunnerFallbackPlans(plans)
	if len(plans) == 0 {
		return h.Runner.Run(ctx, base), imageRunnerFallbackPlan{}
	}

	var lastRes *imagepkg.RunResult
	var lastPlan imageRunnerFallbackPlan
	for idx, plan := range plans {
		opt := base
		applyImageRunnerFallbackPlan(&opt, plan)
		res := h.Runner.Run(ctx, opt)
		lastRes = res
		lastPlan = plan
		if res != nil && res.Status == imagepkg.StatusSuccess {
			return res, plan
		}
		if idx >= len(plans)-1 || res == nil || !shouldContinueImageRunnerFallback(res.ErrorCode) || ctx.Err() != nil {
			return res, plan
		}
		logger.L().Warn("image runner fallback plan failed, try next",
			zap.String("task_id", base.TaskID),
			zap.String("current_plan", imageRunnerFallbackPlanLabel(plan)),
			zap.String("next_plan", imageRunnerFallbackPlanLabel(plans[idx+1])),
			zap.String("error_code", res.ErrorCode),
			zap.String("error_message", res.ErrorMessage))
	}
	if lastRes != nil {
		return lastRes, lastPlan
	}
	return &imagepkg.RunResult{Status: imagepkg.StatusFailed, ErrorCode: imagepkg.ErrUnknown}, imageRunnerFallbackPlan{}
}

func imageRunnerFallbackPlanLabel(plan imageRunnerFallbackPlan) string {
	if isFreeRunnerPlan(plan) {
		return "free"
	}
	if strings.TrimSpace(plan.PreferredPlanType) != "" {
		return plan.PreferredPlanType
	}
	return "any"
}

func imageTraceStepForRunnerPlan(result *imagepkg.RunResult, plan imageRunnerFallbackPlan, status, reasonCode, reasonDetail string) imagepkg.TaskTraceStep {
	return imageTraceStepForRunner(result, isFreeRunnerPlan(plan), status, reasonCode, reasonDetail)
}

func prepareImageRoutes(routes []*channel.Route, policy imageFallbackPolicy) ([]*channel.Route, []imagepkg.TaskTraceStep) {
	if len(routes) == 0 {
		return nil, nil
	}
	type indexedRoute struct {
		index int
		route *channel.Route
	}

	hasAPIMart := false
	for _, rt := range routes {
		if imageProviderForRoute(rt) == imagepkg.TraceProviderAPIMart {
			hasAPIMart = true
			break
		}
	}

	now := time.Now()
	active := make([]indexedRoute, 0, len(routes))
	skipped := make([]imagepkg.TaskTraceStep, 0, len(routes))
	for idx, rt := range routes {
		if rt == nil || rt.Channel == nil {
			continue
		}
		if policy.DisableChannels {
			reason := strings.TrimSpace(policy.DisableChannelReason)
			if reason == "" {
				reason = imageFallbackReasonResolutionRunnerOnly
			}
			skipped = append(skipped, skippedImageRouteStep(rt, reason, "resolution policy uses account runner only"))
			continue
		}
		if policy.SkipCodexToAPIMart && hasAPIMart && imageProviderForRoute(rt) == imagepkg.TraceProviderCodex {
			skipped = append(skipped, skippedImageRouteStep(rt, imageFallbackReasonSkipCodex, "policy skips codex and goes direct to apimart"))
			continue
		}
		if coolingDown, detail := isImageRouteCoolingDown(rt.Channel, policy, now); coolingDown {
			skipped = append(skipped, skippedImageRouteStep(rt, imageFallbackReasonChannelWarmup, detail))
			continue
		}
		active = append(active, indexedRoute{index: idx, route: rt})
	}

	sort.SliceStable(active, func(i, j int) bool {
		left := imageRoutePolicyRank(active[i].route, policy.ChannelOrder)
		right := imageRoutePolicyRank(active[j].route, policy.ChannelOrder)
		if left != right {
			return left < right
		}
		return active[i].index < active[j].index
	})

	ordered := make([]*channel.Route, 0, len(active))
	for _, item := range active {
		ordered = append(ordered, item.route)
	}
	return ordered, skipped
}

func skippedImageRouteStep(rt *channel.Route, reasonCode, reasonDetail string) imagepkg.TaskTraceStep {
	step := imageTraceStepForRoute(rt)
	step.Status = imageFallbackStatusSkipped
	step.ReasonCode = reasonCode
	step.ReasonDetail = reasonDetail
	return step
}

func isImageRouteCoolingDown(ch *channel.Channel, policy imageFallbackPolicy, now time.Time) (bool, string) {
	if ch == nil || policy.ChannelFailThreshold <= 0 || policy.ChannelCooldown <= 0 {
		return false, ""
	}
	if ch.FailCount < policy.ChannelFailThreshold || !ch.LastTestAt.Valid {
		return false, ""
	}
	elapsed := now.Sub(ch.LastTestAt.Time)
	if elapsed >= policy.ChannelCooldown {
		return false, ""
	}
	left := (policy.ChannelCooldown - elapsed).Round(time.Second)
	if left < 0 {
		left = 0
	}
	return true, fmt.Sprintf("fail_count=%d threshold=%d cooldown_left=%s", ch.FailCount, policy.ChannelFailThreshold, left)
}

func imageRoutePolicyRank(rt *channel.Route, order []string) int {
	if len(order) == 0 {
		return 0
	}
	for idx, token := range order {
		if imageRouteMatchesPolicyToken(rt, token) {
			return idx
		}
	}
	return len(order) + 1
}

func imageRouteMatchesPolicyToken(rt *channel.Route, token string) bool {
	if rt == nil || rt.Channel == nil {
		return false
	}
	token = strings.ToLower(strings.TrimSpace(token))
	if token == "" {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(imageProviderForRoute(rt)))
	channelName := strings.ToLower(strings.TrimSpace(rt.Channel.Name))
	channelID := strconv.FormatUint(rt.Channel.ID, 10)
	switch {
	case token == provider:
		return true
	case strings.TrimPrefix(token, "provider:") == provider && strings.HasPrefix(token, "provider:"):
		return true
	case token == channelName:
		return true
	case strings.TrimPrefix(token, "channel:") == channelName && strings.HasPrefix(token, "channel:"):
		return true
	case token == channelID:
		return true
	case strings.TrimPrefix(token, "channel_id:") == channelID && strings.HasPrefix(token, "channel_id:"):
		return true
	default:
		return false
	}
}
