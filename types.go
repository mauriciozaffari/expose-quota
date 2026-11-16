package main

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

type GroupID string
type WindowID string
type Scope string
type Source string
type Validity string

const (
	GroupDefault    GroupID  = "default"
	GroupGemini     GroupID  = "gemini"
	GroupThirdParty GroupID  = "third-party"
	ScopeShared     Scope    = "shared"
	ScopeOpus       Scope    = "opus"
	ScopeSonnet     Scope    = "sonnet"
	ScopeFable      Scope    = "fable"
	SourceDirect    Source   = "direct"
	Valid           Validity = "valid"
)

type Window struct {
	ID                WindowID  `json:"id"`
	Scope             Scope     `json:"scope"`
	RemainingFraction float64   `json:"remaining_fraction"`
	ResetTime         time.Time `json:"reset_time,omitempty"`
	FetchedAt         time.Time `json:"fetched_at"`
	Source            Source    `json:"source"`
	Validity          Validity  `json:"validity"`
}
type Group struct {
	ID      GroupID  `json:"id"`
	Windows []Window `json:"windows"`
}
type Credential struct {
	AuthIndex string
	Provider  string
	JSON      []byte
}
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string][]string
	Body    []byte
}
type HTTPResponse struct {
	StatusCode int
	Body       []byte
}
type HTTPDoer interface {
	Do(context.Context, HTTPRequest) (HTTPResponse, error)
}
type jsonFloat float64

func (f *jsonFloat) UnmarshalJSON(data []byte) error {
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		var s string
		if json.Unmarshal(data, &s) != nil {
			*f = jsonFloat(math.NaN())
			return nil
		}
		n = json.Number(strings.TrimSpace(s))
	}
	v, err := strconv.ParseFloat(string(n), 64)
	if err != nil {
		v = math.NaN()
	}
	*f = jsonFloat(v)
	return nil
}
