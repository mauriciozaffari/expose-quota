package main

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// CredentialState is the transient, in-memory view of one physical credential
// file. Token material never leaves the fetch call and is never logged.
type CredentialState struct {
	Token     string
	ProjectID string
	AccountID string
	Expired   bool
}

// flexTime reads a credential time field. Pinned CPA writes `expired` as an
// RFC3339 string; other dialects use a numeric Unix time (seconds, or
// milliseconds above 1e12). Anything unparseable is zero, meaning unknown.
type flexTime struct{ time.Time }

func (t *flexTime) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(strings.Trim(strings.TrimSpace(string(data)), `"`))
	t.Time = time.Time{}
	if text == "" || text == "null" {
		return nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
		t.Time = parsed
		return nil
	}
	if value, err := strconv.ParseInt(text, 10, 64); err == nil {
		t.Time = unixEpoch(value)
	}
	return nil
}

// unixEpoch interprets a positive integer as milliseconds above 1e12, else
// seconds. Antigravity writes timestamp as UnixMilli.
func unixEpoch(value int64) time.Time {
	const millisecondThreshold = 1_000_000_000_000
	switch {
	case value <= 0:
		return time.Time{}
	case value > millisecondThreshold:
		return time.UnixMilli(value)
	default:
		return time.Unix(value, 0)
	}
}

// flexNumber tolerates a JSON number or quoted integer. Malformed, fractional or
// non-positive values read as zero (absent).
type flexNumber int64

func (n *flexNumber) UnmarshalJSON(data []byte) error {
	trimmed := strings.Trim(strings.TrimSpace(string(data)), `"`)
	*n = 0
	if parsed, err := strconv.ParseInt(trimmed, 10, 64); err == nil && parsed > 0 {
		*n = flexNumber(parsed)
	}
	return nil
}

// ParseCredential extracts the access token and expiry metadata from a raw
// credential file, tolerating flat and nested metadata shapes. Expiry precedence:
// the explicit `expired` time, then `timestamp` + `expires_in` (Antigravity,
// timestamp in Unix milliseconds), then the access-token JWT `exp`. Unknown
// expiry is unusable: no request is attempted.
func ParseCredential(raw []byte, now time.Time) CredentialState {
	var payload struct {
		AccessToken    string     `json:"access_token"`
		AccessTokenAlt string     `json:"accessToken"`
		Expired        flexTime   `json:"expired"`
		ExpiresIn      flexNumber `json:"expires_in"`
		Timestamp      flexNumber `json:"timestamp"`
		ProjectID      string     `json:"project_id"`
		ProjectIDAlt   string     `json:"projectId"`
		AccountID      string     `json:"account_id"`
		AccountIDAlt   string     `json:"accountId"`
		Metadata       struct {
			AccessToken    string     `json:"access_token"`
			AccessTokenAlt string     `json:"accessToken"`
			Expired        flexTime   `json:"expired"`
			ExpiresIn      flexNumber `json:"expires_in"`
			Timestamp      flexNumber `json:"timestamp"`
			ProjectID      string     `json:"project_id"`
			ProjectIDAlt   string     `json:"projectId"`
			AccountID      string     `json:"account_id"`
			AccountIDAlt   string     `json:"accountId"`
		} `json:"metadata"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
		return CredentialState{Expired: true}
	}

	state := CredentialState{
		Token:     firstNonEmpty(payload.AccessToken, payload.AccessTokenAlt, payload.Metadata.AccessToken, payload.Metadata.AccessTokenAlt),
		ProjectID: firstNonEmpty(payload.ProjectID, payload.ProjectIDAlt, payload.Metadata.ProjectID, payload.Metadata.ProjectIDAlt),
		AccountID: firstNonEmpty(payload.AccountID, payload.AccountIDAlt, payload.Metadata.AccountID, payload.Metadata.AccountIDAlt),
	}
	if state.Token == "" {
		return CredentialState{Expired: true}
	}

	expiry := payload.Expired.Time
	if expiry.IsZero() {
		expiry = payload.Metadata.Expired.Time
	}
	if expiry.IsZero() {
		expiry = timestampExpiry(payload.Timestamp, payload.ExpiresIn)
	}
	if expiry.IsZero() {
		expiry = timestampExpiry(payload.Metadata.Timestamp, payload.Metadata.ExpiresIn)
	}
	if expiry.IsZero() {
		expiry = jwtExpiry(state.Token)
	}

	// A 30-second skew keeps a request from being sent with a token that expires
	// in flight.
	state.Expired = expiry.IsZero() || !expiry.After(now.Add(30*time.Second))
	return state
}

// maxCredentialLifetimeSeconds caps expires_in at ten years. A larger duration is
// absurd for an access token; rejecting it also keeps the nanosecond conversion
// from overflowing into a wrapped future expiry.
const maxCredentialLifetimeSeconds = 10 * 365 * 24 * 3600

// timestampExpiry derives expiry from a start instant plus a duration. Both must
// be present; expires_in alone is a duration, not an expiry. An out-of-range
// duration or a non-monotonic result is unknown, which fails closed.
func timestampExpiry(start flexNumber, expiresIn flexNumber) time.Time {
	if start <= 0 || expiresIn <= 0 || expiresIn > maxCredentialLifetimeSeconds {
		return time.Time{}
	}
	began := unixEpoch(int64(start))
	if began.IsZero() {
		return time.Time{}
	}
	expiry := began.Add(time.Duration(expiresIn) * time.Second)
	if !expiry.After(began) {
		return time.Time{}
	}
	return expiry
}

// jwtExpiry decodes the `exp` claim of a compact JWT without verification. The
// payload is read in memory and discarded; no part of the token is logged.
func jwtExpiry(token string) time.Time {
	const jwtParts = 3
	parts := strings.Split(token, ".")
	if len(parts) != jwtParts {
		return time.Time{}
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(decoded, &claims) != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
