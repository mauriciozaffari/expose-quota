package main

import (
	"strconv"
	"testing"
	"time"
)

func Test_parseAntigravityQuota_maps_known_buckets_only(t *testing.T) {
	body := []byte(`{"groups":[
		{"buckets":[
			{"bucketId":"gemini-weekly","remainingFraction":0,"resetTime":"2026-10-13T00:00:00Z"},
			{"bucketId":"gemini-5h","remainingFraction":1,"resetTime":"2026-10-06T05:00:00Z"},
			{"bucketId":"unknown-bucket","remainingFraction":0.5}
		]},
		{"buckets":[{"bucketId":"3p-weekly","remainingFraction":0.25}]}
	]}`)
	groups, err := parseAntigravityQuota(body)
	if err != nil {
		t.Fatalf("parseAntigravityQuota() error = %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	if groups[0].ID != GroupGemini || len(groups[0].Windows) != 2 {
		t.Fatalf("gemini group = %#v", groups[0])
	}
	if groups[1].ID != GroupThirdParty || len(groups[1].Windows) != 1 {
		t.Fatalf("third-party group = %#v", groups[1])
	}
	if groups[0].Windows[0].RemainingFraction != 0 || groups[0].Windows[0].ResetTime.IsZero() {
		t.Fatalf("weekly window = %#v", groups[0].Windows[0])
	}
}

func Test_parseAntigravityQuota_invalid_numbers_fail_open(t *testing.T) {
	body := []byte(`{"groups":[{"buckets":[
		{"bucketId":"gemini-weekly","remainingFraction":"0.5"},
		{"bucketId":"gemini-5h","remainingFraction":null},
		{"bucketId":"3p-weekly","remainingFraction":1.5},
		{"bucketId":"3p-5h"}
	]}]}`)
	groups, err := parseAntigravityQuota(body)
	if err != nil {
		t.Fatalf("parseAntigravityQuota() error = %v", err)
	}
	// Only the quoted in-range value is usable; nothing is invented for the rest.
	if len(groups) != 1 || groups[0].ID != GroupGemini || len(groups[0].Windows) != 1 {
		t.Fatalf("groups = %#v, want one gemini window", groups)
	}
	if got := groups[0].Windows[0].RemainingFraction; got != 0.5 {
		t.Fatalf("remaining = %v, want 0.5", got)
	}
}

func Test_parseClaudeUsage_scopes_and_exclusions(t *testing.T) {
	body := []byte(`{
		"five_hour":{"utilization":0,"resets_at":"2026-10-06T05:00:00Z"},
		"seven_day":{"utilization":100,"resets_at":null},
		"seven_day_opus":{"utilization":50},
		"seven_day_sonnet":{"utilization":"25"},
		"iguana_necktie":{"utilization":10},
		"seven_day_cowork":{"utilization":90},
		"oauth_apps":{"utilization":90},
		"extra_usage":{"utilization":90}
	}`)
	groups, err := parseClaudeUsage(body)
	if err != nil {
		t.Fatalf("parseClaudeUsage() error = %v", err)
	}
	if len(groups) != 1 || groups[0].ID != GroupDefault {
		t.Fatalf("groups = %#v", groups)
	}
	byID := map[WindowID]Window{}
	for _, window := range groups[0].Windows {
		byID[window.ID] = window
	}
	if got := byID["five_hour"].RemainingFraction; got != 1 {
		t.Fatalf("five_hour remaining = %v, want 1", got)
	}
	if got := byID["seven_day"].RemainingFraction; got != 0 {
		t.Fatalf("seven_day remaining = %v, want 0", got)
	}
	if byID["opus"].Scope != ScopeOpus || byID["sonnet"].Scope != ScopeSonnet || byID["fable"].Scope != ScopeFable {
		t.Fatalf("scopes = %#v", byID)
	}
	if _, ok := byID["seven_day_cowork"]; ok {
		t.Fatalf("cowork window must be excluded: %#v", byID)
	}
	if len(byID) != 5 {
		t.Fatalf("windows = %d, want 5", len(byID))
	}
}

func Test_parseClaudeUsage_bad_values_fail_open(t *testing.T) {
	body := []byte(`{"five_hour":{"utilization":150},"seven_day":{"utilization":-1},"seven_day_opus":{}}`)
	groups, err := parseClaudeUsage(body)
	if err != nil {
		t.Fatalf("parseClaudeUsage() error = %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("groups = %#v, want none", groups)
	}
}

func Test_parseCodexUsage_inference_windows_only(t *testing.T) {
	now := time.Now()
	body := []byte(`{"rate_limit":{
		"primary_window":{"used_percent":100,"reset_at":` + unixString(now.Add(time.Hour)) + `,"limit_window_seconds":18000},
		"secondary_window":{"used_percent":0,"reset_after_seconds":3600},
		"code_review_window":{"used_percent":50,"limit_reached":true}
	}}`)
	groups, err := parseCodexUsage(body)
	if err != nil {
		t.Fatalf("parseCodexUsage() error = %v", err)
	}
	if len(groups) != 1 || len(groups[0].Windows) != 2 {
		t.Fatalf("groups = %#v, want primary and secondary", groups)
	}
	if got := groups[0].Windows[0].RemainingFraction; got != 0 {
		t.Fatalf("primary remaining = %v, want 0", got)
	}
	if groups[0].Windows[0].ResetTime.IsZero() {
		t.Fatal("primary reset must be parsed")
	}
	if got := groups[0].Windows[1].RemainingFraction; got != 1 {
		t.Fatalf("secondary remaining = %v, want 1", got)
	}
}

func Test_parseCodexUsage_missing_numeric_usage_is_unknown(t *testing.T) {
	for name, body := range map[string]string{
		"missing used_percent":   `{"rate_limit":{"primary_window":{"limit_reached":true},"secondary_window":{"limit_window_seconds":604800}}}`,
		"malformed used_percent": `{"rate_limit":{"primary_window":{"used_percent":"abc"},"secondary_window":{"used_percent":150}}}`,
		"negative used_percent":  `{"rate_limit":{"primary_window":{"used_percent":-5,"limit_reached":false}}}`,
		"flags only":             `{"rate_limit":{"allowed":false,"limit_reached":true}}`,
	} {
		groups, err := parseCodexUsage([]byte(body))
		if err != nil && name != "malformed used_percent" {
			t.Fatalf("%s: parseCodexUsage() error = %v", name, err)
		}
		if len(groups) != 0 {
			t.Fatalf("%s: groups = %#v, want none; a fraction must never be fabricated", name, groups)
		}
	}
}

// The real captured shape: primary 0% used, secondary 100% used, account-level
// allowed=false and limit_reached=true. Both windows must publish with their own
// numbers and resets, and the provider must read as pressured via the secondary.
func Test_parseCodexUsage_captured_shape_publishes_both_windows(t *testing.T) {
	now := time.Now()
	primaryReset := now.Add(5 * time.Hour)
	secondaryReset := now.Add(5 * 24 * time.Hour)
	body := []byte(`{"rate_limit":{"allowed":false,"limit_reached":true,
		"primary_window":{"used_percent":0,"limit_window_seconds":18000,"reset_at":` + unixString(primaryReset) + `,"reset_after_seconds":18000},
		"secondary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_at":` + unixString(secondaryReset) + `,"reset_after_seconds":432000}}}`)
	groups, err := parseCodexUsage(body)
	if err != nil {
		t.Fatalf("parseCodexUsage() error = %v", err)
	}
	if len(groups) != 1 || len(groups[0].Windows) != 2 {
		t.Fatalf("groups = %#v, want primary and secondary", groups)
	}
	primary, secondary := groups[0].Windows[0], groups[0].Windows[1]
	if primary.ID != "primary" || primary.RemainingFraction != 1 {
		t.Fatalf("primary = %+v, want its own numeric reading (remaining 1)", primary)
	}
	if secondary.ID != "secondary" || secondary.RemainingFraction != 0 {
		t.Fatalf("secondary = %+v, want remaining 0", secondary)
	}
	if primary.ResetTime.Unix() != primaryReset.Unix() || secondary.ResetTime.Unix() != secondaryReset.Unix() {
		t.Fatalf("resets = %v / %v, want %v / %v", primary.ResetTime, secondary.ResetTime, primaryReset, secondaryReset)
	}

}

func Test_parseCodexUsage_healthy_window_is_not_pressured(t *testing.T) {
	body := []byte(`{"rate_limit":{"allowed":true,"limit_reached":false,
		"primary_window":{"used_percent":10,"limit_reached":false,"reset_after_seconds":3600},
		"secondary_window":{"used_percent":10,"reset_after_seconds":3600}}}`)
	groups, err := parseCodexUsage(body)
	if err != nil {
		t.Fatalf("parseCodexUsage() error = %v", err)
	}
	if len(groups) != 1 || len(groups[0].Windows) != 2 {
		t.Fatalf("groups = %#v, want two windows", groups)
	}
	for _, window := range groups[0].Windows {
		if got := window.RemainingFraction; got < 0.8999 || got > 0.9001 {
			t.Fatalf("%s remaining = %v, want 0.9", window.ID, got)
		}
	}

}

// Contradiction rule: used_percent is the reading and is never dropped;
// limit_reached/allowed are authoritative for pressure only. A flag no window
// explains reads as exhaustion, so it can never be mistaken for headroom.
func Test_parseCodexUsage_contradiction_rule(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []float64
	}{
		{"window flag raises a healthier number to exhausted", `{"rate_limit":{"primary_window":{"used_percent":10,"limit_reached":true,"reset_after_seconds":60}}}`, []float64{0}},
		{"window flag false cannot heal a full number", `{"rate_limit":{"primary_window":{"used_percent":100,"limit_reached":false,"reset_after_seconds":60}}}`, []float64{0}},
		{"unexplained account flag exhausts every window", `{"rate_limit":{"limit_reached":true,"primary_window":{"used_percent":10,"reset_after_seconds":60},"secondary_window":{"used_percent":20,"reset_after_seconds":60}}}`, []float64{0, 0}},
		{"allowed=false with headroom exhausts every window", `{"rate_limit":{"allowed":false,"primary_window":{"used_percent":10,"reset_after_seconds":60}}}`, []float64{0}},
		{"flag explained by an exhausted window keeps the other reading", `{"rate_limit":{"limit_reached":true,"primary_window":{"used_percent":10,"reset_after_seconds":60},"secondary_window":{"used_percent":100,"reset_after_seconds":60}}}`, []float64{0.9, 0}},
	}
	for _, tc := range cases {
		groups, err := parseCodexUsage([]byte(tc.body))
		if err != nil {
			t.Fatalf("%s: error = %v", tc.name, err)
		}
		if len(groups) != 1 || len(groups[0].Windows) != len(tc.want) {
			t.Fatalf("%s: groups = %#v, want %d windows", tc.name, groups, len(tc.want))
		}
		for i, want := range tc.want {
			if got := groups[0].Windows[i].RemainingFraction; got < want-0.0001 || got > want+0.0001 {
				t.Fatalf("%s: window %d remaining = %v, want %v", tc.name, i, got, want)
			}
		}
	}
}

func unixString(value time.Time) string {
	return strconv.FormatInt(value.Unix(), 10)
}

// A window that carries only reset_after_seconds (no reset_at) must still yield
// a reset derived from that duration. The captured dashboard shape normally
// sends reset_at, so this pins the fallback on its own.
func Test_parseCodexUsage_reset_after_seconds_alone_sets_reset(t *testing.T) {
	now := time.Now()
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":50,"limit_window_seconds":18000,"reset_after_seconds":3600}}}`)
	groups, err := parseCodexUsage(body)
	if err != nil {
		t.Fatalf("parseCodexUsage() error = %v", err)
	}
	if len(groups) != 1 || len(groups[0].Windows) != 1 {
		t.Fatalf("groups = %#v, want one primary window", groups)
	}
	reset := groups[0].Windows[0].ResetTime
	if reset.IsZero() {
		t.Fatal("reset_after_seconds alone must produce a reset, got zero")
	}
	want := now.Add(3600 * time.Second)
	if delta := reset.Sub(want); delta < -5*time.Second || delta > 5*time.Second {
		t.Fatalf("reset = %v, want within 5s of %v", reset, want)
	}
}

// A window whose own limit_reached flag conflicts with its number is exhausted,
// and that explanation must leave the sibling window at its own reading. When
// the own-flag explanation is ignored, the account-level flag is wrongly treated
// as unexplained and the healthy sibling is forced to zero.
func Test_parseCodexUsage_own_flag_explains_account_flag(t *testing.T) {
	now := time.Now()
	body := []byte(`{"rate_limit":{"limit_reached":true,
		"primary_window":{"used_percent":0,"limit_reached":true,"reset_at":` + unixString(now.Add(time.Hour)) + `},
		"secondary_window":{"used_percent":10,"reset_at":` + unixString(now.Add(24*time.Hour)) + `}}}`)
	groups, err := parseCodexUsage(body)
	if err != nil {
		t.Fatalf("parseCodexUsage() error = %v", err)
	}
	if len(groups) != 1 || len(groups[0].Windows) != 2 {
		t.Fatalf("groups = %#v, want primary and secondary", groups)
	}
	primary, secondary := groups[0].Windows[0], groups[0].Windows[1]
	if primary.RemainingFraction != 0 {
		t.Fatalf("primary remaining = %v, want 0 from its own limit_reached", primary.RemainingFraction)
	}
	if secondary.RemainingFraction != 0.9 {
		t.Fatalf("secondary remaining = %v, want 0.9: an explained account flag must not exhaust a healthy sibling", secondary.RemainingFraction)
	}
}
