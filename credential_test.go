package main

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"testing"
	"time"
)

// Real on-disk shapes (confirmed against live credential files): `expired` is an
// RFC3339 string with offset; Antigravity `timestamp` is Unix milliseconds with an
// `expires_in` duration in seconds; Claude and Codex carry `last_refresh` only.
func Test_ParseCredential_real_shapes(t *testing.T) {
	now := time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	farFuture := now.Add(24 * 365 * time.Hour)

	cases := []struct {
		name string
		raw  string
		want bool // Expired
	}{
		{"claude fresh opaque token RFC3339 Z", `{"access_token":"sk-ant-oat01-opaque","expired":"` + future.Format(time.RFC3339) + `","last_refresh":"` + past.Format(time.RFC3339) + `"}`, false},
		{"claude expired RFC3339", `{"access_token":"sk-ant-oat01-opaque","expired":"` + past.Format(time.RFC3339) + `"}`, true},
		{"claude fresh +08:00 offset", `{"access_token":"tok","expired":"` + future.In(time.FixedZone("x", 8*3600)).Format(time.RFC3339) + `"}`, false},
		{"claude expired +08:00 offset", `{"access_token":"tok","expired":"` + past.In(time.FixedZone("x", 8*3600)).Format(time.RFC3339) + `"}`, true},
		{"nanosecond RFC3339", `{"access_token":"tok","expired":"` + future.Format(time.RFC3339Nano) + `"}`, false},
		{"antigravity expired with ms timestamp", `{"access_token":"tok","expired":"` + past.Format(time.RFC3339) + `","timestamp":` + jsonNumber(past.Add(-time.Hour).UnixMilli()) + `,"expires_in":3599}`, true},
		{"antigravity fresh RFC3339", `{"access_token":"tok","expired":"` + future.Format(time.RFC3339) + `","timestamp":` + jsonNumber(now.UnixMilli()) + `,"expires_in":3599}`, false},
		{"antigravity ms timestamp plus expires_in only, expired", `{"access_token":"tok","timestamp":` + jsonNumber(past.Add(-time.Hour).UnixMilli()) + `,"expires_in":3599}`, true},
		{"antigravity ms timestamp plus expires_in only, fresh", `{"access_token":"tok","timestamp":` + jsonNumber(now.UnixMilli()) + `,"expires_in":3599}`, false},
		{"seconds timestamp plus expires_in", `{"access_token":"tok","timestamp":` + jsonNumber(now.Unix()) + `,"expires_in":3600}`, false},
		{"explicit expired wins over a fresh timestamp", `{"access_token":"tok","expired":"` + past.Format(time.RFC3339) + `","timestamp":` + jsonNumber(now.UnixMilli()) + `,"expires_in":3599}`, true},
		{"nested metadata RFC3339", `{"metadata":{"access_token":"tok","expired":"` + future.Format(time.RFC3339) + `"}}`, false},
		{"far future", `{"access_token":"tok","expired":"` + farFuture.Format(time.RFC3339) + `"}`, false},
		{"skew boundary inside 30s is expired", `{"access_token":"tok","expired":"` + now.Add(20*time.Second).Format(time.RFC3339) + `"}`, true},
		{"missing token", `{"expired":"` + future.Format(time.RFC3339) + `"}`, true},
		{"malformed expired, no fallback", `{"access_token":"tok","expired":"not a time"}`, true},
		{"empty expired, no fallback", `{"access_token":"tok","expired":""}`, true},
		{"null expired", `{"access_token":"tok","expired":null}`, true},
		{"expires_in without timestamp", `{"access_token":"tok","expires_in":3600}`, true},
		{"malformed JSON", `not json`, true},
		{"empty", ``, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			state := ParseCredential([]byte(testCase.raw), now)
			if state.Expired != testCase.want {
				t.Fatalf("Expired = %v, want %v", state.Expired, testCase.want)
			}
		})
	}
}

// Numeric `expired` is a legacy dialect kept as a fallback.
func Test_ParseCredential_numeric_expired_fallback(t *testing.T) {
	now := time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"seconds future", `{"access_token":"tok","expired":` + jsonNumber(future.Unix()) + `}`, false},
		{"seconds past", `{"access_token":"tok","expired":` + jsonNumber(past.Unix()) + `}`, true},
		{"quoted seconds future", `{"access_token":"tok","expired":"` + jsonNumber(future.Unix()) + `"}`, false},
		{"milliseconds future", `{"access_token":"tok","expired":` + jsonNumber(future.UnixMilli()) + `}`, false},
		{"milliseconds past", `{"access_token":"tok","expired":` + jsonNumber(past.UnixMilli()) + `}`, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ParseCredential([]byte(testCase.raw), now).Expired; got != testCase.want {
				t.Fatalf("Expired = %v, want %v", got, testCase.want)
			}
		})
	}
}

func Test_ParseCredential_codex_jwt_with_RFC3339(t *testing.T) {
	now := time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)
	token := jwtWithExpiry(now.Add(time.Hour))
	raw := `{"access_token":"` + token + `","expired":"` + now.Add(time.Hour).Format(time.RFC3339) + `","last_refresh":"` + now.Add(-time.Hour).Format(time.RFC3339) + `","account_id":"acct-1"}`
	state := ParseCredential([]byte(raw), now)
	if state.Expired || state.AccountID != "acct-1" {
		t.Fatalf("state = %#v, want usable with account", state)
	}
}

func Test_ParseCredential_jwt_expiry_is_the_last_resort(t *testing.T) {
	now := time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)
	if ParseCredential([]byte(`{"access_token":"`+jwtWithExpiry(now.Add(time.Hour))+`"}`), now).Expired {
		t.Fatal("future JWT must be usable when no expiry field exists")
	}
	if !ParseCredential([]byte(`{"access_token":"`+jwtWithExpiry(now.Add(-time.Hour))+`"}`), now).Expired {
		t.Fatal("past JWT must be unusable")
	}
	// A malformed expired field falls back to the JWT.
	if ParseCredential([]byte(`{"access_token":"`+jwtWithExpiry(now.Add(time.Hour))+`","expired":"garbage"}`), now).Expired {
		t.Fatal("a malformed expired must fall back to the JWT exp")
	}
}

func Test_ParseCredential_project_and_account_metadata(t *testing.T) {
	raw := []byte(`{"access_token":"t","expired":"2999-01-01T00:00:00Z","metadata":{"project_id":"p1","account_id":"a1"}}`)
	state := ParseCredential(raw, time.Now())
	if state.ProjectID != "p1" || state.AccountID != "a1" || state.Expired {
		t.Fatalf("state = %#v", state)
	}
}

func jsonNumber(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func jwtWithExpiry(expiry time.Time) string {
	payload, _ := json.Marshal(map[string]int64{"exp": expiry.Unix()})
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func Test_ParseCredential_expires_in_overflow_fails_closed(t *testing.T) {
	now := time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)
	start := now.UnixMilli()
	cases := []struct {
		name      string
		expiresIn int64
		want      bool // Expired
	}{
		{"max int64", math.MaxInt64, true},
		{"overflows when multiplied by 1e9", 20_000_000_000_000_000, true},
		{"negative", -3600, true},
		{"zero", 0, true},
		{"above ten years", 400_000_000, true},
		{"normal duration", 3599, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			raw := `{"access_token":"sk-ant-oat01-opaque","timestamp":` + jsonNumber(start) + `,"expires_in":` + jsonNumber(testCase.expiresIn) + `}`
			state := ParseCredential([]byte(raw), now)
			if state.Expired != testCase.want {
				t.Fatalf("Expired = %v (expiry would be %v), want %v", state.Expired, state.Expired, testCase.want)
			}
		})
	}
}
