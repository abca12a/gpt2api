package codexusage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Options struct {
	AuthDir string
	LogPath string
	Now     func() time.Time
}

type Service struct {
	authDir string
	logPath string
	now     func() time.Time
}

type Snapshot struct {
	StatsAvailable bool                     `json:"stats_available"`
	GeneratedAt    time.Time                `json:"generated_at"`
	Summary        Summary                  `json:"summary"`
	ByEmail        map[string]*AccountUsage `json:"-"`
}

type Summary struct {
	StatsAvailable        bool `json:"stats_available"`
	ActiveAccounts        int  `json:"active_accounts"`
	DisabledAccounts      int  `json:"disabled_accounts"`
	RequestsToday         int  `json:"requests_today"`
	SuccessToday          int  `json:"success_today"`
	FailedToday           int  `json:"failed_today"`
	Quota429EventsToday   int  `json:"quota_429_events_today"`
	Quota429AccountsToday int  `json:"quota_429_accounts_today"`
}

type AccountUsage struct {
	StatsAvailable   bool       `json:"stats_available"`
	ExternalPool     bool       `json:"external_pool"`
	ExternalDisabled bool       `json:"external_disabled"`
	ExternalPlan     string     `json:"external_plan"`
	RequestsToday    int        `json:"requests_today"`
	SuccessToday     int        `json:"success_today"`
	FailedToday      int        `json:"failed_today"`
	Quota429Today    int        `json:"quota_429_today"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
}

type authIdentity struct {
	email    string
	plan     string
	disabled bool
}

var (
	logPrefixRE = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})\] \[([^\]]+)\]`)
	useAuthRE   = regexp.MustCompile(`Use OAuth provider=codex auth_file=([^\s]+) for model`)
	imageLogRE  = regexp.MustCompile(`\b(\d{3})\s+\|.*POST\s+"(/v1/images/[^"]+)"`)
)

func New(opt Options) *Service {
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		authDir: strings.TrimSpace(opt.AuthDir),
		logPath: strings.TrimSpace(opt.LogPath),
		now:     now,
	}
}

func (s *Service) Snapshot(ctx context.Context) (*Snapshot, error) {
	now := s.now()
	snap := &Snapshot{
		GeneratedAt: now,
		ByEmail:     map[string]*AccountUsage{},
	}
	if s == nil || s.authDir == "" || s.logPath == "" {
		return snap, nil
	}

	authByFile, err := loadAuthIdentities(s.authDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snap, nil
		}
		return nil, err
	}
	for _, ident := range authByFile {
		usage := ensureUsage(snap.ByEmail, ident.email)
		usage.StatsAvailable = true
		usage.ExternalPool = true
		usage.ExternalPlan = ident.plan
		usage.ExternalDisabled = ident.disabled
	}

	if err := parseLog(ctx, s.logPath, now, authByFile, snap); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			snap.StatsAvailable = true
			snap.Summary.StatsAvailable = true
			populateSummary(snap)
			return snap, nil
		}
		return nil, err
	}
	populateSummary(snap)
	return snap, nil
}

func loadAuthIdentities(authDir string) (map[string]authIdentity, error) {
	entries, err := os.ReadDir(authDir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]authIdentity, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "codex-") || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		ident, ok := readAuthIdentity(filepath.Join(authDir, name), name)
		if !ok {
			continue
		}
		out[name] = ident
	}
	return out, nil
}

func readAuthIdentity(path, name string) (authIdentity, bool) {
	ident := authIdentity{email: emailFromAuthFileName(name), plan: planFromAuthFileName(name)}
	data, err := os.ReadFile(path)
	if err == nil {
		var raw struct {
			Email    string `json:"email"`
			Disabled bool   `json:"disabled"`
		}
		if json.Unmarshal(data, &raw) == nil {
			if strings.TrimSpace(raw.Email) != "" {
				ident.email = strings.TrimSpace(raw.Email)
			}
			ident.disabled = raw.Disabled
		}
	}
	ident.email = normalizeEmail(ident.email)
	if ident.email == "" {
		return authIdentity{}, false
	}
	return ident, true
}

func parseLog(ctx context.Context, logPath string, now time.Time, authByFile map[string]authIdentity, snap *Snapshot) error {
	f, err := os.Open(logPath)
	if err != nil {
		return err
	}
	defer f.Close()

	day := now.Format("2006-01-02")
	ridAuth := map[string]authIdentity{}
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 4*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := scanner.Text()
		logTime, requestID, ok := parseLogPrefix(line)
		if !ok || logTime.Format("2006-01-02") != day {
			continue
		}
		if m := useAuthRE.FindStringSubmatch(line); len(m) == 2 {
			if ident, ok := authByFile[filepath.Base(m[1])]; ok {
				ridAuth[requestID] = ident
			}
			continue
		}
		ident, ok := ridAuth[requestID]
		if !ok || ident.email == "" {
			continue
		}
		usage := ensureUsage(snap.ByEmail, ident.email)
		usage.StatsAvailable = true
		usage.ExternalPool = true
		usage.ExternalPlan = ident.plan
		usage.ExternalDisabled = ident.disabled
		if strings.Contains(line, "request error, error status: 429") {
			usage.Quota429Today++
			continue
		}
		if !strings.Contains(line, "gin_logger.go") || !strings.Contains(line, `POST    "/v1/images/`) {
			continue
		}
		m := imageLogRE.FindStringSubmatch(line)
		if len(m) != 3 {
			continue
		}
		usage.RequestsToday++
		if m[1] == "200" {
			usage.SuccessToday++
		} else {
			usage.FailedToday++
		}
		captured := logTime
		usage.LastUsedAt = &captured
	}
	return scanner.Err()
}

func parseLogPrefix(line string) (time.Time, string, bool) {
	m := logPrefixRE.FindStringSubmatch(line)
	if len(m) != 4 {
		return time.Time{}, "", false
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1]+" "+m[2], time.Local)
	if err != nil {
		return time.Time{}, "", false
	}
	requestID := strings.TrimSpace(m[3])
	if requestID == "" || requestID == "--------" {
		return time.Time{}, "", false
	}
	return t, requestID, true
}

func populateSummary(snap *Snapshot) {
	if snap == nil {
		return
	}
	snap.StatsAvailable = true
	snap.Summary.StatsAvailable = true
	quota429Accounts := 0
	for _, usage := range snap.ByEmail {
		if usage == nil || !usage.ExternalPool {
			continue
		}
		if usage.ExternalDisabled {
			snap.Summary.DisabledAccounts++
		} else {
			snap.Summary.ActiveAccounts++
		}
		snap.Summary.RequestsToday += usage.RequestsToday
		snap.Summary.SuccessToday += usage.SuccessToday
		snap.Summary.FailedToday += usage.FailedToday
		snap.Summary.Quota429EventsToday += usage.Quota429Today
		if usage.Quota429Today > 0 {
			quota429Accounts++
		}
	}
	snap.Summary.Quota429AccountsToday = quota429Accounts
}

func ensureUsage(byEmail map[string]*AccountUsage, email string) *AccountUsage {
	email = normalizeEmail(email)
	if email == "" {
		return &AccountUsage{}
	}
	if usage := byEmail[email]; usage != nil {
		return usage
	}
	usage := &AccountUsage{}
	byEmail[email] = usage
	return usage
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func emailFromAuthFileName(name string) string {
	name = filepath.Base(name)
	name = strings.TrimPrefix(name, "codex-")
	for _, suffix := range []string{"-plus.json", "-team.json", "-free.json", ".json"} {
		if strings.HasSuffix(strings.ToLower(name), suffix) {
			return strings.TrimSuffix(name, name[len(name)-len(suffix):])
		}
	}
	return name
}

func planFromAuthFileName(name string) string {
	lower := strings.ToLower(filepath.Base(name))
	for _, plan := range []string{"plus", "team", "free"} {
		if strings.HasSuffix(lower, "-"+plan+".json") {
			return plan
		}
	}
	return ""
}

func SortedEmails(snap *Snapshot) []string {
	if snap == nil {
		return nil
	}
	emails := make([]string, 0, len(snap.ByEmail))
	for email := range snap.ByEmail {
		emails = append(emails, email)
	}
	sort.Strings(emails)
	return emails
}
