package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type account struct {
	Status string  `json:"status"`
	Groups []Group `json:"groups"`
}
type providerReport struct {
	Provider string    `json:"provider"`
	Status   string    `json:"status"`
	Accounts []account `json:"accounts"`
}

var readers = []AccountReader{AntigravityAccountReader{}, ClaudeAccountReader{}, CodexAccountReader{}}

// minPluginSchemaVersion is the oldest host schema this plugin can speak: the
// first version whose management responses return JSON bodies unescaped (see
// pluginapi.ManagementResponse). Older hosts HTML-escape response strings,
// which would corrupt quota.json for consumers.
const minPluginSchemaVersion = 6

type credentialHost interface {
	ListAuthFiles(context.Context) ([]pluginapi.HostAuthFileEntry, error)
	GetAuthFile(context.Context, string) (pluginapi.HostAuthGetResponse, error)
}

var host credentialHost = &cgoHostBridge{}
var quotaTransport HTTPDoer

func init() { quotaTransport, _ = NewTransport("") }

func envelope(v any) ([]byte, int) {
	b, err := json.Marshal(struct {
		OK     bool `json:"ok"`
		Result any  `json:"result"`
	}{true, v})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"internal","message":"internal error"}}`), 1
	}
	return b, 0
}
func failure() ([]byte, int) {
	return []byte(`{"ok":false,"error":{"code":"invalid_request","message":"invalid request"}}`), 1
}
func handle(method string, raw []byte) ([]byte, int) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var req struct {
			SchemaVersion uint32 `json:"schema_version"`
		}
		if json.Unmarshal(raw, &req) != nil || req.SchemaVersion < minPluginSchemaVersion || req.SchemaVersion > pluginabi.SchemaVersion {
			return failure()
		}
		return envelope(struct {
			SchemaVersion uint32             `json:"schema_version"`
			Metadata      pluginapi.Metadata `json:"metadata"`
			Capabilities  map[string]bool    `json:"capabilities"`
		}{req.SchemaVersion, pluginapi.Metadata{Name: "expose-quota", Version: "0.2.1", Author: "Mauricio Zaffari", GitHubRepository: "https://github.com/mauriciozaffari/expose-quota"}, map[string]bool{"management_api": true}})
	case pluginabi.MethodPluginShutdown:
		return envelope(struct{}{})
	case pluginabi.MethodManagementRegister:
		return envelope(struct {
			Resources []struct {
				Path        string `json:"Path"`
				Description string `json:"Description"`
			} `json:"resources"`
		}{[]struct {
			Path        string `json:"Path"`
			Description string `json:"Description"`
		}{{"/quota.json", "Redacted quota snapshots"}}})
	case pluginabi.MethodManagementHandle:
		var req pluginapi.ManagementRequest
		if json.Unmarshal(raw, &req) != nil {
			return failure()
		}
		if req.Method != "GET" || req.Path != "/v0/resource/plugins/expose-quota/quota.json" {
			return failure()
		}
		return quotaResponse(req)
	default:
		return failure()
	}
}
func quotaResponse(req pluginapi.ManagementRequest) ([]byte, int) {
	headers := http.Header{"Content-Type": []string{"application/json"}, "Cache-Control": []string{"no-store"}}
	respond := func(status int, data any) ([]byte, int) {
		body, _ := json.Marshal(data)
		return envelope(pluginapi.ManagementResponse{StatusCode: status, Headers: headers, Body: body})
	}
	key := os.Getenv("CPA_API_KEY")
	auth := strings.Fields(req.Headers.Get("Authorization"))
	if key == "" {
		return respond(403, map[string]string{"error": "not_authorized"})
	}
	if len(auth) != 2 || !strings.EqualFold(auth[0], "Bearer") || auth[1] == "" {
		return respond(401, map[string]string{"error": "missing_credential"})
	}
	a, b := sha256.Sum256([]byte(key)), sha256.Sum256([]byte(auth[1]))
	if subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		return respond(403, map[string]string{"error": "not_authorized"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reports := readReports(ctx, host, quotaTransport)
	return respond(200, struct {
		SchemaVersion int              `json:"schema_version"`
		Generated     time.Time        `json:"generated_at"`
		Providers     []providerReport `json:"providers"`
	}{1, time.Now().UTC(), reports})
}
func readReports(ctx context.Context, h credentialHost, transport HTTPDoer) []providerReport {
	reports := make([]providerReport, 0, len(readers))
	entries, err := h.ListAuthFiles(ctx)
	for _, reader := range readers {
		report := providerReport{Provider: reader.Provider(), Status: "unknown", Accounts: []account{}}
		if err != nil {
			reports = append(reports, report)
			continue
		}
		report.Status = "ok"
		for _, entry := range entries {
			if entry.Provider != reader.Provider() || entry.Disabled {
				continue
			}
			item := account{Status: "unknown", Groups: []Group{}}
			if entry.AuthIndex != "" && ctx.Err() == nil && transport != nil {
				credential, e := h.GetAuthFile(ctx, entry.AuthIndex)
				if e == nil {
					groups, e := reader.Read(ctx, Credential{JSON: credential.JSON}, transport)
					if e == nil && len(groups) > 0 {
						for i := range groups {
							for j := range groups[i].Windows {
								if reader.Provider() == "antigravity" {
									groups[i].Windows[j].ID = "bucket"
								}
							}
						}
						item.Status = "known"
						item.Groups = groups
					}
				}
			}
			report.Accounts = append(report.Accounts, item)
		}
		reports = append(reports, report)
	}
	return reports
}
