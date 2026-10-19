package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"opencode-go-cliproxyapi/internal/catalog"
	"opencode-go-cliproxyapi/internal/config"
)

// logRecorder answers host.http.do with a fixed docs page and records every
// host.log payload.
type logRecorder struct {
	mu   sync.Mutex
	logs [][]byte
	body string
}

func (r *logRecorder) call(method string, payload []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodHostLog:
		r.mu.Lock()
		r.logs = append(r.logs, append([]byte(nil), payload...))
		r.mu.Unlock()
		return hostOK(map[string]any{}), nil
	case pluginabi.MethodHostHTTPDo:
		return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(r.body)}), nil
	}
	return hostOK(map[string]any{}), nil
}

func (r *logRecorder) logCalls() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.logs...)
}

// TestRefreshUnsupportedLogQuotesNewlineIDs pins F2: a docs row whose model
// ID carries control characters must reach the host.log "models" field
// quoted (%q), so no raw newline can forge log lines (CWE-117).
func TestRefreshUnsupportedLogQuotesNewlineIDs(t *testing.T) {
	const evilID = "evil\n2027-01-01 ERROR forged host.log line"
	cfg, err := config.Load([]byte("api-keys:\n  - value: sk-refresh-evil-key\n"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	rec := &logRecorder{body: docPage(testDocBase, docModel{evilID, "@ai-sdk/unknown"})}
	bridge := NewHostBridge(rec.call)
	mgr := catalog.New(cfg, bridge)
	if err := refreshOnce(context.Background(), mgr, bridge, time.Second); err != nil {
		t.Fatalf("refreshOnce: %v", err)
	}
	logCalls := rec.logCalls()
	if len(logCalls) != 1 {
		t.Fatalf("host.log calls = %d, want exactly one unsupported-models warn", len(logCalls))
	}
	var req struct {
		Level   string         `json:"level"`
		Message string         `json:"message"`
		Fields  map[string]any `json:"fields"`
	}
	if err := json.Unmarshal(logCalls[0], &req); err != nil {
		t.Fatalf("decode host.log payload: %v", err)
	}
	if req.Message != "unsupported models excluded from routable catalog" {
		t.Fatalf("unexpected log message: %q", req.Message)
	}
	modelsField, _ := req.Fields["models"].(string)
	if strings.ContainsRune(modelsField, '\n') {
		t.Fatalf("models log field contains raw newline (log injection): %q", modelsField)
	}
	if want := fmt.Sprintf("%q: ", evilID); !strings.Contains(modelsField, want) {
		t.Fatalf("models field must quote the upstream ID; got %q, want prefix %q", modelsField, want)
	}
}
