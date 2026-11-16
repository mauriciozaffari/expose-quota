package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// AccountReader reads normalized included-quota windows for one provider
// credential. It never returns paid-credit values and never sums windows.
type AccountReader interface {
	Provider() string
	Read(ctx context.Context, credential Credential, do HTTPDoer) ([]Group, error)
}

const (
	antigravityQuotaDailyURL   = "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
	antigravityQuotaSandboxURL = "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary"
	antigravityQuotaProdURL    = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
	antigravityQuotaUserAgent  = "antigravity/cli/1.0.13 (aidev_client; os_type=darwin; arch=arm64)"

	claudeUsageURL       = "https://api.anthropic.com/api/oauth/usage"
	claudeUsageUserAgent = "claude-cli/2.1.280 (external, cli)"
	claudeOAuthBeta      = "oauth-2025-04-20"

	codexUsageURL       = "https://chatgpt.com/backend-api/wham/usage"
	codexUsageUserAgent = "codex-tui/0.149.1 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.149.1)"
)

// AntigravityAccountReader reads the Gemini and shared third-party buckets from
// retrieveUserQuotaSummary. loadCodeAssist paid credits are never placement input.
type AntigravityAccountReader struct{}

func (AntigravityAccountReader) Provider() string { return "antigravity" }

func (AntigravityAccountReader) Read(ctx context.Context, credential Credential, do HTTPDoer) ([]Group, error) {
	state := ParseCredential(credential.JSON, time.Now())
	if state.Expired {
		return nil, errors.New("antigravity: credential unusable")
	}
	if state.ProjectID == "" {
		return nil, errors.New("antigravity: credential has no project")
	}
	body, _ := json.Marshal(map[string]string{"project": state.ProjectID})

	// Fallback hosts are tried only when the daily host reports the endpoint is
	// unavailable. Any other status is a real answer, not a routing problem.
	for _, endpoint := range []string{antigravityQuotaDailyURL, antigravityQuotaSandboxURL, antigravityQuotaProdURL} {
		response, err := do.Do(ctx, HTTPRequest{
			Method: http.MethodPost,
			URL:    endpoint,
			Headers: map[string][]string{
				"Authorization": {"Bearer " + state.Token},
				"Accept":        {"*/*"},
				"Content-Type":  {"application/json"},
				"User-Agent":    {antigravityQuotaUserAgent},
			},
			Body: body,
		})
		if err != nil {
			return nil, err
		}
		if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotImplemented {
			continue
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return nil, fmt.Errorf("antigravity quota status %d", response.StatusCode)
		}
		return parseAntigravityQuota(response.Body)
	}
	return nil, errors.New("antigravity: no quota endpoint available")
}

type antigravityQuotaResponse struct {
	Groups []struct {
		Buckets []struct {
			BucketID          string     `json:"bucketId"`
			RemainingFraction *jsonFloat `json:"remainingFraction"`
			ResetTime         string     `json:"resetTime"`
		} `json:"buckets"`
	} `json:"groups"`
}

// parseAntigravityQuota maps known bucket IDs to Gemini or shared third-party
// windows. Unknown bucket IDs are ignored: a new upstream bucket must not be
// guessed into an existing placement group.
func parseAntigravityQuota(body []byte) ([]Group, error) {
	var payload antigravityQuotaResponse
	if json.Unmarshal(body, &payload) != nil {
		return nil, errors.New("antigravity: decode quota response")
	}
	fetchedAt := time.Now()
	buckets := map[GroupID][]Window{}
	for _, group := range payload.Groups {
		for _, bucket := range group.Buckets {
			id := strings.ToLower(strings.TrimSpace(bucket.BucketID))
			var target GroupID
			switch {
			case strings.HasPrefix(id, "gemini-"):
				target = GroupGemini
			case strings.HasPrefix(id, "3p-"), strings.HasPrefix(id, "third-party-"):
				target = GroupThirdParty
			default:
				continue
			}
			if !validWindowID(id) {
				continue
			}
			if bucket.RemainingFraction == nil {
				continue
			}
			fraction := float64(*bucket.RemainingFraction)
			if !validFraction(fraction) {
				continue
			}
			buckets[target] = append(buckets[target], Window{
				ID:                WindowID(id),
				Scope:             ScopeShared,
				RemainingFraction: fraction,
				ResetTime:         parseResetTime(bucket.ResetTime),
				FetchedAt:         fetchedAt,
				Source:            SourceDirect,
				Validity:          Valid,
			})
		}
	}
	return groupsFromBuckets(buckets), nil
}

// ClaudeAccountReader reads the OAuth usage windows: shared five-hour and weekly
// windows plus model-scoped windows. Unrelated product buckets are excluded.
type ClaudeAccountReader struct{}

func (ClaudeAccountReader) Provider() string { return "claude" }

func (ClaudeAccountReader) Read(ctx context.Context, credential Credential, do HTTPDoer) ([]Group, error) {
	state := ParseCredential(credential.JSON, time.Now())
	if state.Expired {
		return nil, errors.New("claude: credential unusable")
	}
	response, err := do.Do(ctx, HTTPRequest{
		Method: http.MethodGet,
		URL:    claudeUsageURL,
		Headers: map[string][]string{
			"Authorization":  {"Bearer " + state.Token},
			"Accept":         {"application/json"},
			"User-Agent":     {claudeUsageUserAgent},
			"anthropic-beta": {claudeOAuthBeta},
		},
	})
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("claude usage status %d", response.StatusCode)
	}
	return parseClaudeUsage(response.Body)
}

// claudeWindowIDs maps the upstream usage keys to normalized window IDs and
// scopes. Every other key (cowork, oauth apps, extra usage) is ignored.
var claudeWindowIDs = []struct {
	key   string
	id    WindowID
	scope Scope
}{
	{"five_hour", "five_hour", ScopeShared},
	{"seven_day", "seven_day", ScopeShared},
	{"seven_day_opus", "opus", ScopeOpus},
	{"seven_day_sonnet", "sonnet", ScopeSonnet},
	{"iguana_necktie", "fable", ScopeFable},
}

type claudeUsageWindow struct {
	Utilization *jsonFloat `json:"utilization"`
	ResetsAt    string     `json:"resets_at"`
}

func parseClaudeUsage(body []byte) ([]Group, error) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return nil, errors.New("claude: decode usage response")
	}
	fetchedAt := time.Now()
	windows := make([]Window, 0, len(claudeWindowIDs))
	for _, known := range claudeWindowIDs {
		raw, ok := payload[known.key]
		if !ok {
			continue
		}
		var window claudeUsageWindow
		if json.Unmarshal(raw, &window) != nil || window.Utilization == nil {
			continue
		}
		remaining, ok := remainingFromPercent(float64(*window.Utilization))
		if !ok {
			continue
		}
		windows = append(windows, Window{
			ID:                known.id,
			Scope:             known.scope,
			RemainingFraction: remaining,
			ResetTime:         parseResetTime(window.ResetsAt),
			FetchedAt:         fetchedAt,
			Source:            SourceDirect,
			Validity:          Valid,
		})
	}
	if len(windows) == 0 {
		return nil, nil
	}
	return []Group{{ID: GroupDefault, Windows: windows}}, nil
}

// CodexAccountReader reads inference rate-limit windows only. Code-review windows
// are excluded from placement.
type CodexAccountReader struct{}

func (CodexAccountReader) Provider() string { return "codex" }

func (CodexAccountReader) Read(ctx context.Context, credential Credential, do HTTPDoer) ([]Group, error) {
	state := ParseCredential(credential.JSON, time.Now())
	if state.Expired {
		return nil, errors.New("codex: credential unusable")
	}
	headers := map[string][]string{
		"Authorization": {"Bearer " + state.Token},
		"Accept":        {"application/json"},
		"User-Agent":    {codexUsageUserAgent},
	}
	if state.AccountID != "" {
		// The OAuth email is not a Codex account ID; only the physical field is used.
		headers["Chatgpt-Account-Id"] = []string{state.AccountID}
	}
	response, err := do.Do(ctx, HTTPRequest{Method: http.MethodGet, URL: codexUsageURL, Headers: headers})
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("codex usage status %d", response.StatusCode)
	}
	return parseCodexUsage(response.Body)
}

type codexRateLimit struct {
	PrimaryWindow    *codexUsageWindow `json:"primary_window"`
	SecondaryWindow  *codexUsageWindow `json:"secondary_window"`
	PrimaryAlt       *codexUsageWindow `json:"primary"`
	SecondaryAlt     *codexUsageWindow `json:"secondary"`
	LimitReached     *bool             `json:"limit_reached"`
	Allowed          *bool             `json:"allowed"`
	CodeReviewWindow *codexUsageWindow `json:"code_review_window"`
}

type codexUsageResponse struct {
	RateLimit *codexRateLimit `json:"rate_limit"`
}

type codexUsageWindow struct {
	UsedPercent       *jsonFloat `json:"used_percent"`
	ResetAt           flexNumber `json:"reset_at"`
	ResetAfterSeconds flexNumber `json:"reset_after_seconds"`
	LimitWindowSecs   flexNumber `json:"limit_window_seconds"`
	LimitReached      *bool      `json:"limit_reached"`
}

func parseCodexUsage(body []byte) ([]Group, error) {
	var payload codexUsageResponse
	if json.Unmarshal(body, &payload) != nil {
		return nil, errors.New("codex: decode usage response")
	}
	if payload.RateLimit == nil {
		return nil, nil
	}
	rate := payload.RateLimit
	pairs := []struct {
		id     WindowID
		window *codexUsageWindow
	}{
		{"primary", firstWindow(rate.PrimaryWindow, rate.PrimaryAlt)},
		{"secondary", firstWindow(rate.SecondaryWindow, rate.SecondaryAlt)},
	}
	fetchedAt := time.Now()
	windows := make([]Window, 0, len(pairs))
	exhausted := false
	for _, pair := range pairs {
		if pair.window == nil {
			continue
		}
		remaining, reset, ok := codexWindowReading(*pair.window, fetchedAt)
		if !ok {
			continue
		}
		// A window's own limit_reached flag outranks a healthier number: the
		// flag is authoritative for pressure, so a contradictory window reads as
		// exhausted rather than as headroom.
		if pair.window.LimitReached != nil && *pair.window.LimitReached && remaining > 0 {
			remaining = 0
		}
		if remaining == 0 {
			exhausted = true
		}
		windows = append(windows, Window{
			ID:                pair.id,
			Scope:             ScopeShared,
			RemainingFraction: remaining,
			ResetTime:         reset,
			FetchedAt:         fetchedAt,
			Source:            SourceDirect,
			Validity:          Valid,
		})
	}
	if len(windows) == 0 {
		return nil, nil
	}
	// limit_reached/allowed=false at rate-limit level describe the account, not
	// one window, so they are consistent whenever any window is exhausted (the
	// captured shape: primary 0% used, secondary 100% used). Only when no window
	// explains the flag is it a contradiction; then the flag wins for pressure
	// and every published window reads as exhausted.
	accountBlocked := (rate.LimitReached != nil && *rate.LimitReached) || (rate.Allowed != nil && !*rate.Allowed)
	if accountBlocked && !exhausted {
		for i := range windows {
			windows[i].RemainingFraction = 0
		}
	}
	return []Group{{ID: GroupDefault, Windows: windows}}, nil
}

// codexWindowReading converts one window from its numeric fields only:
// used_percent gives the remaining fraction and reset_at or reset_after_seconds
// the reset. A missing or malformed used_percent is unknown (no window); no
// fraction is ever invented. Flags are applied by parseCodexUsage, never here,
// so a contradictory flag can raise pressure but cannot drop the reading.
func codexWindowReading(window codexUsageWindow, now time.Time) (float64, time.Time, bool) {
	if window.UsedPercent == nil {
		return 0, time.Time{}, false
	}
	remaining, ok := remainingFromPercent(float64(*window.UsedPercent))
	if !ok {
		return 0, time.Time{}, false
	}
	reset := unixResetValue(int64(window.ResetAt), now)
	if reset.IsZero() && window.ResetAfterSeconds > 0 && window.ResetAfterSeconds <= 30*24*3600 {
		reset = now.Add(time.Duration(window.ResetAfterSeconds) * time.Second)
	}
	return remaining, reset, true
}

func firstWindow(primary, fallback *codexUsageWindow) *codexUsageWindow {
	if primary != nil {
		return primary
	}
	return fallback
}

// remainingFromPercent converts a 0..100 utilization/used value to a remaining
// fraction. Percentages are converted here, at the parser boundary; the cache
// never stores percentages.
func remainingFromPercent(used float64) (float64, bool) {
	if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 {
		return 0, false
	}
	remaining := 1 - used/100
	if !validFraction(remaining) {
		return 0, false
	}
	return remaining, true
}

func validFraction(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func validWindowID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func groupsFromBuckets(buckets map[GroupID][]Window) []Group {
	order := []GroupID{GroupGemini, GroupThirdParty, GroupDefault}
	groups := make([]Group, 0, len(buckets))
	for _, id := range order {
		if len(buckets[id]) > 0 {
			groups = append(groups, Group{ID: id, Windows: buckets[id]})
		}
	}
	return groups
}

// parseResetTime accepts an ISO-8601 reset or a numeric Unix timestamp. A missing
// or unparseable reset is nullable, not an error.
func parseResetTime(raw string) time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed.UTC()
	}
	var numeric flexNumber
	if json.Unmarshal([]byte(trimmed), &numeric) == nil && numeric > 0 {
		return time.Unix(int64(numeric), 0)
	}
	return time.Time{}
}

func unixResetValue(seconds int64, now time.Time) time.Time {
	if seconds <= now.Unix() || seconds > now.Add(30*24*time.Hour).Unix() {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}
