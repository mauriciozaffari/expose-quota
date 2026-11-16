package pluginhost

import (
	"context"
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalExposeQuotaLifecycle(t *testing.T) {
	authDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(authDir, "a.json"), []byte(`{"type":"claude"}`), 0600); err != nil {
		t.Fatal(err)
	}
	enabled := true
	cfg := &config.Config{AuthDir: authDir, Plugins: config.PluginsConfig{Enabled: true, Dir: os.Getenv("EXPOSE_QUOTA_TEST_DIR"), Configs: map[string]config.PluginInstanceConfig{"expose-quota": {Enabled: &enabled}}}}
	h := New()
	defer h.ShutdownAll()
	ctx := context.Background()
	for cycle := 0; cycle < 2; cycle++ {
		h.ApplyConfig(ctx, cfg)
		if !h.PluginLoaded("expose-quota") {
			t.Fatalf("cycle %d: plugin not loaded", cycle)
		}
		h.RegisterManagementRoutes(ctx, nil)
		req := httptest.NewRequest(http.MethodGet, "/v0/resource/plugins/expose-quota/quota.json", nil)
		req.Header.Set("Authorization", "Bearer test-key")
		w := httptest.NewRecorder()
		if !h.ServeResourceHTTP(w, req) || w.Code != 200 {
			t.Fatalf("cycle %d: status=%d body=%s", cycle, w.Code, w.Body.String())
		}
		var data struct {
			Providers []struct {
				Provider string `json:"provider"`
				Accounts []struct {
					Status string `json:"status"`
				} `json:"accounts"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Providers) != 3 || len(data.Providers[1].Accounts) != 1 || data.Providers[1].Accounts[0].Status != "unknown" {
			t.Fatalf("cycle %d: callback failed: %s", cycle, w.Body.String())
		}
		if !h.UnloadPlugin("expose-quota") {
			t.Fatalf("cycle %d: unload failed", cycle)
		}
	}
}
