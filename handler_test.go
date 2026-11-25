package main

import (
	"context"
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAuthAndRedaction(t *testing.T) {
	t.Setenv("CPA_API_KEY", "client-secret")
	for _, tc := range []struct {
		key  string
		want int
	}{{"", 401}, {"wrong", 403}, {"client-secret", 200}} {
		req := pluginapi.ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/expose-quota/quota.json", Headers: http.Header{"Authorization": []string{"Bearer " + tc.key}}}
		raw, _ := json.Marshal(req)
		result, code := handle("management.handle", raw)
		if code != 0 {
			t.Fatalf("call failed")
		}
		var env struct {
			Result pluginapi.ManagementResponse `json:"result"`
		}
		if json.Unmarshal(result, &env) != nil || env.Result.StatusCode != tc.want {
			t.Fatalf("status: %s", result)
		}
	}
}

type fakeHost struct {
	entries []pluginapi.HostAuthFileEntry
	creds   map[string]pluginapi.HostAuthGetResponse
	calls   int
}

func (f *fakeHost) ListAuthFiles(context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	f.calls++
	return f.entries, nil
}
func (f *fakeHost) GetAuthFile(_ context.Context, id string) (pluginapi.HostAuthGetResponse, error) {
	f.calls++
	return f.creds[id], nil
}

type fakeHTTP struct{}

func (fakeHTTP) Do(_ context.Context, req HTTPRequest) (HTTPResponse, error) {
	return HTTPResponse{StatusCode: 200, Body: []byte(`{"five_hour":{"utilization":25},"untrusted":"oauth-secret"}`)}, nil
}
func TestMultipleUnknownAndRedaction(t *testing.T) {
	valid := []byte(`{"access_token":"oauth-secret","expired":"2099-01-01T00:00:00Z"}`)
	stale := []byte(`{"access_token":"stale-secret","expired":"2000-01-01T00:00:00Z"}`)
	h := &fakeHost{entries: []pluginapi.HostAuthFileEntry{{Provider: "claude", AuthIndex: "account-secret-1"}, {Provider: "claude", AuthIndex: "account-secret-2"}, {Provider: "claude", AuthIndex: "account-secret-3"}}, creds: map[string]pluginapi.HostAuthGetResponse{"account-secret-1": {JSON: valid}, "account-secret-2": {JSON: stale}}}
	reports := readReports(context.Background(), h, fakeHTTP{})
	b, _ := json.Marshal(reports)
	s := string(b)
	for _, secret := range []string{"oauth-secret", "stale-secret", "account-secret"} {
		if strings.Contains(s, secret) {
			t.Fatal("secret exposed")
		}
	}
	if len(reports[1].Accounts) != 3 || reports[1].Accounts[0].Status != "known" || reports[1].Accounts[1].Status != "unknown" || reports[1].Accounts[2].Status != "unknown" {
		t.Fatalf("unexpected reports %s", s)
	}
}
func TestCanonicalSnapshotWireFormat(t *testing.T) {
	oldHost, oldTransport := host, quotaTransport
	host = &fakeHost{entries: []pluginapi.HostAuthFileEntry{{Provider: "claude", AuthIndex: "one"}}, creds: map[string]pluginapi.HostAuthGetResponse{"one": {JSON: []byte(`{"access_token":"secret","expired":"2099-01-01T00:00:00Z"}`)}}}
	quotaTransport = fakeHTTP{}
	t.Cleanup(func() { host, quotaTransport = oldHost, oldTransport })
	t.Setenv("CPA_API_KEY", "test-key")
	raw, status := quotaResponse(pluginapi.ManagementRequest{Headers: http.Header{"Authorization": []string{"Bearer test-key"}}})
	var envelope struct {
		Result pluginapi.ManagementResponse `json:"result"`
	}
	if status != 0 || json.Unmarshal(raw, &envelope) != nil || envelope.Result.StatusCode != 200 {
		t.Fatal("request failed")
	}
	var document struct {
		SchemaVersion int `json:"schema_version"`
		Providers     []struct {
			Accounts []struct {
				Groups []struct {
					Windows []map[string]any `json:"windows"`
				} `json:"groups"`
			} `json:"accounts"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(envelope.Result.Body, &document); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != 1 || len(document.Providers) < 2 || len(document.Providers[1].Accounts) != 1 || len(document.Providers[1].Accounts[0].Groups) != 1 || len(document.Providers[1].Accounts[0].Groups[0].Windows) == 0 {
		t.Fatal("missing canonical window")
	}
	window := document.Providers[1].Accounts[0].Groups[0].Windows[0]
	if _, ok := window["remaining_fraction"]; !ok {
		t.Fatal("missing remaining fraction")
	}
	if _, ok := window["reset_at"]; !ok {
		t.Fatal("missing reset_at")
	}
	for _, field := range []string{"reset_time", "validity", "source", "fetched_at", "scope"} {
		if _, ok := window[field]; ok {
			t.Fatalf("unexpected legacy field %s", field)
		}
	}
}

func TestCanonicalWindowFields(t *testing.T) {
	reset := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	data, err := json.Marshal(Window{ID: "weekly", RemainingFraction: 0.5, ResetTime: reset})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["name"] != "weekly" || fields["reset_at"] != "2030-01-02T03:04:05Z" {
		t.Fatalf("unexpected canonical fields: %s", data)
	}
}

func TestAuthFailsBeforeHost(t *testing.T) {
	old := host
	h := &fakeHost{}
	host = h
	t.Cleanup(func() { host = old })
	t.Setenv("CPA_API_KEY", "")
	_, _ = quotaResponse(pluginapi.ManagementRequest{Headers: http.Header{"Authorization": []string{"Bearer anything"}}})
	if h.calls != 0 {
		t.Fatal("host called without authorization")
	}
}

func TestRegistrationMetadata(t *testing.T) {
	raw, status := handle("plugin.register", []byte(`{"schema_version":6}`))
	var result struct {
		Result struct {
			Metadata pluginapi.Metadata `json:"metadata"`
		} `json:"result"`
	}
	if status != 0 || json.Unmarshal(raw, &result) != nil || result.Result.Metadata.Author == "" || result.Result.Metadata.GitHubRepository == "" {
		t.Fatal("registration lacks required metadata")
	}
}
