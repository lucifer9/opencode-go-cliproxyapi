package catalog

import (
	"context"
	"errors"
	"fmt"
	"html"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"opencode-go-cliproxyapi/internal/config"
)

const (
	testDocURL = "https://opencode.ai/v2/docs/console/go"
	testBase   = "https://opencode.ai/inference/go"
)

// fakeClient is a canned, thread-safe HostClient capturing the last request.
type fakeClient struct {
	mu     sync.Mutex
	resp   pluginapi.HTTPResponse
	err    error
	gotReq *pluginapi.HTTPRequest
}

func (f *fakeClient) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotReq = &req
	return f.resp, f.err
}

func (f *fakeClient) set(resp pluginapi.HTTPResponse, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resp, f.err = resp, err
}

func testCfg() config.Config {
	return config.Config{
		ModelsDocURL:     testDocURL,
		ModelPrefix:      config.ModelPrefix{Enabled: true, Value: "opencode-go"},
		MaxResponseBytes: 1 << 20,
		Catalog:          config.Catalog{StaleWhileUnavailable: true},
		Protocols:        config.Protocols{ChatCompletions: true, Messages: true, Responses: true, Gemini: true},
	}
}

// Row builders mirror the docs table: each package's real endpoint.
func chat(id string) docRow {
	return docRow{Name: id, ID: id, Endpoint: testBase + "/openai/v1/chat/completions", Package: "@ai-sdk/openai-compatible"}
}

func msgs(id string) docRow {
	return docRow{Name: id, ID: id, Endpoint: testBase + "/anthropic/v1/messages", Package: "@ai-sdk/anthropic"}
}

func resp(id string) docRow {
	return docRow{Name: id, ID: id, Endpoint: testBase + "/openai/v1/responses", Package: "@ai-sdk/openai"}
}

func gem(id string) docRow {
	return docRow{Name: id, ID: id, Endpoint: testBase + "/google/v1beta/models/" + id, Package: "@ai-sdk/google"}
}

// docPage renders rows as the docs page Endpoints table.
func docPage(rows ...docRow) []byte {
	var b strings.Builder
	b.WriteString(`<html><body><h2 id="endpoints">Endpoints</h2><table><thead><tr><th>Model</th><th>Model ID</th><th>Endpoint</th><th>AI SDK Package</th></tr></thead><tbody>`)
	for _, r := range rows {
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td><code>%s</code></td><td><code>%s</code></td></tr>`,
			html.EscapeString(r.Name), html.EscapeString(r.ID), html.EscapeString(r.Endpoint), html.EscapeString(r.Package))
	}
	b.WriteString(`</tbody></table></body></html>`)
	return []byte(b.String())
}

func okDoc(rows ...docRow) pluginapi.HTTPResponse {
	return pluginapi.HTTPResponse{StatusCode: 200, Body: docPage(rows...)}
}

func newManager(cfg config.Config, fc *fakeClient) *Manager {
	return New(cfg, fc)
}

func mustRefresh(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: unexpected error %v", err)
	}
}

func findModel(t *testing.T, models []ModelRecord, upstreamID string) ModelRecord {
	t.Helper()
	for _, mo := range models {
		if mo.UpstreamID == upstreamID {
			return mo
		}
	}
	t.Fatalf("model %q not found in %+v", upstreamID, models)
	return ModelRecord{}
}

func TestRouteEndpointPath(t *testing.T) {
	cases := []struct {
		route Route
		want  string
	}{
		{RouteChatCompletions, "/openai/v1/chat/completions"},
		{RouteMessages, "/anthropic/v1/messages"},
		{RouteResponses, "/openai/v1/responses"},
		{RouteGemini, "/google/v1beta/models"},
		{Route("bogus"), ""},
	}
	for _, c := range cases {
		if got := c.route.EndpointPath(); got != c.want {
			t.Errorf("%q.EndpointPath() = %q, want %q", c.route, got, c.want)
		}
	}
}

// The docs page is public: the refresh must not send any credential.
func TestRefreshRequestShape(t *testing.T) {
	fc := &fakeClient{resp: okDoc(chat("glm-5.3"))}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)
	req := fc.gotReq
	if req.Method != "GET" || req.URL != testDocURL {
		t.Errorf("request = %s %q, want GET %q", req.Method, req.URL, testDocURL)
	}
	if got := req.Headers.Get("Accept"); got != "text/html" {
		t.Errorf("Accept = %q", got)
	}
	for _, h := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"} {
		if got := req.Headers.Get(h); got != "" {
			t.Errorf("%s header sent to the docs page: %q", h, got)
		}
	}
}

func TestRefreshSuccess(t *testing.T) {
	fc := &fakeClient{resp: okDoc(
		docRow{Name: "GPT 5.6 Luna", ID: "gpt-5.6-luna", Endpoint: testBase + "/openai/v1/responses", Package: "@ai-sdk/openai"},
		docRow{ID: "glm-5.3", Endpoint: testBase + "/openai/v1/chat/completions", Package: "@ai-sdk/openai-compatible"},
		docRow{Name: "Claude Haiku 5.5", ID: "claude-haiku-5-5", Endpoint: testBase + "/anthropic/v1/messages", Package: "@ai-sdk/anthropic"},
	)}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)

	want := []ModelRecord{
		{PublicID: "opencode-go/gpt-5.6-luna", UpstreamID: "gpt-5.6-luna", DisplayName: "GPT 5.6 Luna", Protocol: RouteResponses, UpstreamURL: testBase + "/openai/v1/responses"},
		{PublicID: "opencode-go/glm-5.3", UpstreamID: "glm-5.3", DisplayName: "glm-5.3", Protocol: RouteChatCompletions, UpstreamURL: testBase + "/openai/v1/chat/completions"},
		{PublicID: "opencode-go/claude-haiku-5-5", UpstreamID: "claude-haiku-5-5", DisplayName: "Claude Haiku 5.5", Protocol: RouteMessages, UpstreamURL: testBase + "/anthropic/v1/messages"},
	}
	if got := m.Models(); !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %+v\nwant %+v", got, want)
	}
	if len(m.Unsupported()) != 0 || len(m.Warnings()) != 0 {
		t.Errorf("unexpected diagnostics: %v %v", m.Unsupported(), m.Warnings())
	}
}

// Only packages with an adapter are routable; others are excluded with the
// package named in diagnostics.
func TestUnsupportedPackageExcluded(t *testing.T) {
	fc := &fakeClient{resp: okDoc(
		chat("glm-5.3"),
		docRow{ID: "grok-x", Endpoint: testBase + "/xai/v1/chat", Package: "@ai-sdk/xai"},
		docRow{ID: "no-package", Endpoint: testBase + "/openai/v1/chat/completions"},
	)}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)

	if got := m.Models(); len(got) != 1 || got[0].UpstreamID != "glm-5.3" {
		t.Fatalf("models = %+v, want only glm-5.3", got)
	}
	want := []UnsupportedModel{
		{UpstreamID: "grok-x", Reason: `unsupported AI SDK package "@ai-sdk/xai"`},
		{UpstreamID: "no-package", Reason: `unsupported AI SDK package ""`},
	}
	if got := m.Unsupported(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unsupported = %+v, want %+v", got, want)
	}
}

// API keys and prompts go to the row's URL, so it must stay on the docs
// page's scheme and host (spec 05 §2 trust boundary).
func TestEndpointOffDocsHostExcluded(t *testing.T) {
	const reason = "endpoint is not on the docs host"
	cases := []struct {
		name     string
		endpoint string
		routable bool
	}{
		{"same host", "https://opencode.ai/inference/go/openai/v1/chat/completions", true},
		{"host case-insensitive", "https://OPENCODE.ai/inference/go/openai/v1/chat/completions", true},
		{"other host", "https://evil.example/openai/v1/chat/completions", false},
		{"subdomain", "https://api.opencode.ai/openai/v1/chat/completions", false},
		{"scheme downgrade", "http://opencode.ai/inference/go/openai/v1/chat/completions", false},
		{"userinfo", "https://opencode.ai@evil.example/x", false},
		{"userinfo same host", "https://user@opencode.ai/x", false},
		{"query", "https://opencode.ai/x?key=1", false},
		{"fragment", "https://opencode.ai/x#y", false},
		{"relative", "/inference/go/openai/v1/chat/completions", false},
		{"empty", "", false},
		{"unparseable", "https://opencode.ai/\x7f", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := chat("m")
			row.Endpoint = c.endpoint
			m := newManager(testCfg(), &fakeClient{resp: okDoc(row)})
			mustRefresh(t, m)
			if c.routable {
				if got := findModel(t, m.Models(), "m"); got.UpstreamURL != c.endpoint {
					t.Fatalf("UpstreamURL = %q, want %q", got.UpstreamURL, c.endpoint)
				}
				return
			}
			if got := m.Models(); len(got) != 0 {
				t.Fatalf("off-host endpoint still routable: %+v", got)
			}
			if _, ok := m.Lookup("m"); ok {
				t.Fatal("off-host record must not resolve via Lookup")
			}
			unsup := m.Unsupported()
			if len(unsup) != 1 || unsup[0].UpstreamID != "m" || unsup[0].Reason != reason {
				t.Fatalf("unsupported = %+v, want m excluded with %q", unsup, reason)
			}
		})
	}
}

func TestDedupFirstWins(t *testing.T) {
	fc := &fakeClient{resp: okDoc(chat("glm-5.3"), msgs("glm-5.3"))}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)

	models := m.Models()
	if len(models) != 1 || models[0].Protocol != RouteChatCompletions {
		t.Fatalf("models = %+v, want the first glm-5.3 row only", models)
	}
	warns := m.Warnings()
	if len(warns) != 1 || !strings.Contains(warns[0], `"glm-5.3"`) {
		t.Errorf("warnings = %v, want duplicate diagnostic", warns)
	}
}

func TestMissingIDSkippedAndDiagnosed(t *testing.T) {
	fc := &fakeClient{resp: okDoc(chat(""), chat("glm-5.3"))}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)

	if len(m.Models()) != 1 {
		t.Fatalf("got %d models, want 1", len(m.Models()))
	}
	unsup := m.Unsupported()
	if len(unsup) != 1 || unsup[0].UpstreamID != "" || unsup[0].Reason != "missing id" {
		t.Errorf("unsupported = %+v, want missing-id diagnostic", unsup)
	}
}

func TestRefreshErrorsKeepStaleSnapshot(t *testing.T) {
	cases := []struct {
		name     string
		resp     pluginapi.HTTPResponse
		err      error
		maxBytes int64
		wantCat  string
	}{
		{"http 429", pluginapi.HTTPResponse{StatusCode: 429, Body: []byte("{}")}, nil, 1 << 20, "http 429"},
		{"http 500", pluginapi.HTTPResponse{StatusCode: 500}, nil, 1 << 20, "http 500"},
		{"table missing", pluginapi.HTTPResponse{StatusCode: 200, Body: []byte("<html><p>moved</p></html>")}, nil, 1 << 20, "endpoints table not found"},
		{"table empty", okDoc(), nil, 1 << 20, "endpoints table has no rows"},
		{"transport error", pluginapi.HTTPResponse{}, errors.New("dial tcp: refused"), 1 << 20, "network error"},
		{"oversized body", pluginapi.HTTPResponse{StatusCode: 200, Body: make([]byte, catalogBudgetFloor+1)}, nil, 64, "response exceeds max-response-bytes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testCfg()
			cfg.MaxResponseBytes = c.maxBytes
			fc := &fakeClient{resp: okDoc(resp("gpt-5.6-luna"))}
			m := newManager(cfg, fc)
			mustRefresh(t, m)

			fc.set(c.resp, c.err)
			err := m.Refresh(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), c.wantCat) {
				t.Errorf("error %q missing category %q", err, c.wantCat)
			}
			if got := m.Models(); len(got) != 1 || got[0].UpstreamID != "gpt-5.6-luna" {
				t.Errorf("stale snapshot lost: %+v", got)
			}
		})
	}
}

// TestFetchBudgetFloor pins the catalogBudgetFloor fix: an undersized
// max-response-bytes must not make every refresh fail permanently when the
// docs page is larger than the knob (with stale-while-unavailable:false that
// would wipe the routable set each tick). The fetch check uses
// max(MaxResponseBytes, floor); genuinely oversized bodies (>floor) are
// still rejected.
func TestFetchBudgetFloor(t *testing.T) {
	rows := []docRow{chat("glm-5.3")}
	for range 64 {
		rows = append(rows, chat("glm-5.3")) // duplicates pad the page
	}
	rows = append(rows, chat("glm-5.2"))
	page := docPage(rows...)
	if len(page) <= 1024 {
		t.Fatalf("precondition: page %d bytes should exceed the knob", len(page))
	}
	cfg := testCfg()
	cfg.MaxResponseBytes = 1024
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: page}}
	m := newManager(cfg, fc)
	mustRefresh(t, m)
	models := m.Models()
	if len(models) != 2 || models[0].UpstreamID != "glm-5.3" || models[1].UpstreamID != "glm-5.2" {
		t.Fatalf("undersized max-response-bytes broke refresh: %+v", models)
	}

	// A body exceeding the floor stays rejected even with a small knob.
	fc.set(pluginapi.HTTPResponse{StatusCode: 200, Body: make([]byte, catalogBudgetFloor+1)}, nil)
	err := m.Refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "response exceeds max-response-bytes") {
		t.Fatalf("oversized body (>floor) accepted: err=%v", err)
	}
}

// TestRefreshNilClientClassifiedError pins that a Manager built without a
// host client (direct construction only; production always wires the bridge)
// fails Refresh through the classified path instead of panicking on Do.
func TestRefreshNilClientClassifiedError(t *testing.T) {
	m := New(testCfg(), nil)
	err := m.Refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "host client unavailable") {
		t.Fatalf("nil client must yield classified error, got %v", err)
	}
	if len(m.Models()) != 0 {
		t.Fatalf("nil-client refresh must not fabricate models: %+v", m.Models())
	}
}

func TestRefreshErrorClearsWhenNoStale(t *testing.T) {
	cfg := testCfg()
	cfg.Catalog.StaleWhileUnavailable = false
	fc := &fakeClient{resp: okDoc(resp("gpt-5.6-luna"), chat(""))}
	m := newManager(cfg, fc)
	mustRefresh(t, m)

	fc.set(pluginapi.HTTPResponse{StatusCode: 503}, nil)
	clearErr := m.Refresh(context.Background())
	if clearErr == nil || !strings.Contains(clearErr.Error(), "http 503") {
		t.Fatalf("clear-path error = %v, want http 503 category", clearErr)
	}
	if got := m.Models(); len(got) != 0 {
		t.Errorf("routable set not cleared: %+v", got)
	}
	if got := m.Unsupported(); len(got) != 0 {
		t.Errorf("diagnostics not cleared: %+v", got)
	}
	// FR-002: a cleared snapshot must stop resolving previously-valid IDs.
	if _, ok := m.Lookup("gpt-5.6-luna"); ok {
		t.Fatal("Lookup still resolves IDs from the cleared snapshot")
	}

	// Next success restores the routable set.
	fc.set(okDoc(chat("glm-5.3")), nil)
	mustRefresh(t, m)
	if got := m.Models(); len(got) != 1 || got[0].UpstreamID != "glm-5.3" {
		t.Errorf("recovery failed: %+v", got)
	}
}

func TestPublicIDWithoutPrefix(t *testing.T) {
	cfg := testCfg()
	cfg.ModelPrefix.Enabled = false
	m := newManager(cfg, &fakeClient{resp: okDoc(chat("glm-5.3"))})
	mustRefresh(t, m)
	if got := m.Models()[0].PublicID; got != "glm-5.3" {
		t.Errorf("PublicID = %q, want bare id", got)
	}
}

// TestLookupByPrefixedBareAndMiss pins O(1) ID resolution over the swap
// index: public (prefixed) and bare upstream IDs resolve; misses do not.
func TestLookupByPrefixedBareAndMiss(t *testing.T) {
	fc := &fakeClient{resp: okDoc(chat("glm-5.3"), resp("gpt-5.6-luna"))}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)

	rec, ok := m.Lookup("opencode-go/glm-5.3")
	if !ok || rec.UpstreamID != "glm-5.3" || rec.Protocol != RouteChatCompletions {
		t.Errorf("prefixed lookup = %+v %v", rec, ok)
	}
	rec, ok = m.Lookup("glm-5.3")
	if !ok || rec.PublicID != "opencode-go/glm-5.3" {
		t.Errorf("bare lookup = %+v %v", rec, ok)
	}
	rec, ok = m.Lookup("gpt-5.6-luna")
	if !ok || rec.PublicID != "opencode-go/gpt-5.6-luna" {
		t.Errorf("bare lookup second model = %+v %v", rec, ok)
	}
	if _, ok := m.Lookup("no-such-model"); ok {
		t.Error("miss must not resolve")
	}
	if _, ok := m.Lookup(""); ok {
		t.Error("empty id must not resolve")
	}

	// Lookup must track the refreshed snapshot.
	fc.set(okDoc(chat("kimi-k3")), nil)
	mustRefresh(t, m)
	if _, ok := m.Lookup("glm-5.3"); ok {
		t.Error("replaced snapshot must not serve stale lookups")
	}
}

// TestSeedFromServesOldRecordsWithNewCfg pins the F5 fix: SeedFrom gives m
// prev's last-good snapshot rebuilt under m's OWN cfg — prefix-off here, so
// seeded PublicIDs are bare where prev's were prefixed — and subsequent
// refreshes use m's models-doc-url while prev stays untouched.
func TestSeedFromServesOldRecordsWithNewCfg(t *testing.T) {
	prev := newManager(testCfg(), &fakeClient{resp: okDoc(chat("glm-5.3"), chat("glm-5.3"), chat(""))})
	mustRefresh(t, prev)

	cfg := testCfg()
	cfg.ModelsDocURL = "https://opencode.ai/docs/go"
	cfg.ModelPrefix.Enabled = false
	fc := &fakeClient{}
	m := newManager(cfg, fc)
	m.SeedFrom(prev)

	got := m.Models()
	if len(got) != 1 || got[0].UpstreamID != "glm-5.3" || got[0].PublicID != "glm-5.3" {
		t.Fatalf("seeded models = %+v, want glm-5.3 with recomputed bare PublicID", got)
	}
	if _, ok := m.Lookup("opencode-go/glm-5.3"); ok {
		t.Error("prev's prefixed public id must not resolve under m's prefix-off cfg")
	}
	if gotU, wantU := m.Unsupported(), prev.Unsupported(); !reflect.DeepEqual(gotU, wantU) {
		t.Errorf("seeded unsupported = %+v, want %+v", gotU, wantU)
	}
	if gotW, wantW := m.Warnings(), prev.Warnings(); !reflect.DeepEqual(gotW, wantW) {
		t.Errorf("seeded warnings = %v, want %v", gotW, wantW)
	}

	// The next refresh uses m's own cfg; prev keeps its snapshot untouched.
	fc.set(okDoc(chat("kimi-k3")), nil)
	mustRefresh(t, m)
	if fc.gotReq.URL != "https://opencode.ai/docs/go" {
		t.Errorf("refresh URL = %q, want the seeded manager's own models-doc-url", fc.gotReq.URL)
	}
	if got := m.Models(); len(got) != 1 || got[0].PublicID != "kimi-k3" {
		t.Errorf("post-seed refresh models = %+v, want prefix-off kimi-k3", got)
	}
	if got := prev.Models(); len(got) != 1 || got[0].UpstreamID != "glm-5.3" {
		t.Errorf("prev mutated by seeding: %+v", got)
	}
}

// TestSeedFromRevalidatesEndpointsAgainstNewDocHost pins seed-time host
// revalidation: rows that pointed at the old docs host stop serving once
// seeded into a manager whose models-doc-url lives on another host.
func TestSeedFromRevalidatesEndpointsAgainstNewDocHost(t *testing.T) {
	prev := newManager(testCfg(), &fakeClient{resp: okDoc(chat("glm-5.3"))})
	mustRefresh(t, prev)

	cfg := testCfg()
	cfg.ModelsDocURL = "https://mirror.test/docs/go"
	m := newManager(cfg, &fakeClient{})
	m.SeedFrom(prev)

	if got := m.Models(); len(got) != 0 {
		t.Fatalf("old-host rows still routable after seed: %+v", got)
	}
	want := []UnsupportedModel{{UpstreamID: "glm-5.3", Reason: "endpoint is not on the docs host"}}
	if got := m.Unsupported(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unsupported = %+v, want %+v", got, want)
	}
	findModel(t, prev.Models(), "glm-5.3") // seeding must not mutate prev
}

func TestRouteCountsViaModels(t *testing.T) {
	fc := &fakeClient{resp: okDoc(
		chat("glm-5.3"), chat("kimi-k3"),
		msgs("minimax-m3"), msgs("qwen3.8-max"), msgs("claude-haiku-5-5"),
		resp("gpt-5.6-luna"),
		gem("gemini-4-argon-contributor"),
	)}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)
	counts := map[Route]int{}
	for _, mo := range m.Models() {
		counts[mo.Protocol]++
	}
	if counts[RouteChatCompletions] != 2 || counts[RouteMessages] != 3 || counts[RouteResponses] != 1 || counts[RouteGemini] != 1 {
		t.Errorf("counts = %v", counts)
	}
}

func TestModelsReturnCopies(t *testing.T) {
	m := newManager(testCfg(), &fakeClient{resp: okDoc(resp("gpt-5.6-luna"))})
	mustRefresh(t, m)

	got := m.Models()
	got[0].UpstreamID = "mutated"
	got[0] = ModelRecord{}
	if fresh := m.Models(); fresh[0].UpstreamID != "gpt-5.6-luna" {
		t.Errorf("snapshot mutated through returned slice: %+v", fresh[0])
	}

	unsup := m.Unsupported()
	_ = append(unsup, UnsupportedModel{UpstreamID: "x"})
	if len(m.Unsupported()) != 0 {
		t.Error("Unsupported not returning a copy")
	}
	warns := m.Warnings()
	_ = append(warns, "x")
	if len(m.Warnings()) != 0 {
		t.Error("Warnings not returning a copy")
	}
}

func TestConcurrentRefreshAndReads(t *testing.T) {
	fc := &fakeClient{}
	m := newManager(testCfg(), fc)

	var writerWG, readerWG sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 3; r++ {
		writerWG.Add(1)
		go func(r int) {
			defer writerWG.Done()
			for i := 0; i < 50; i++ {
				if i%10 == 0 {
					fc.set(pluginapi.HTTPResponse{StatusCode: 500}, nil)
				} else {
					fc.set(okDoc(chat(fmt.Sprintf("glm-%d", r))), nil)
				}
				_ = m.Refresh(context.Background())
			}
		}(r)
	}
	for r := 0; r < 3; r++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = m.Models()
					_ = m.Unsupported()
					_ = m.Warnings()
				}
			}
		}()
	}
	writerWG.Wait() // writers are bounded; then release readers
	close(stop)
	readerWG.Wait()
}

func TestProtocolFlagsExcludeRoutes(t *testing.T) {
	routes := []struct {
		route    Route
		disable  func(p *config.Protocols)
		excluded string
		routable []string
	}{
		{RouteChatCompletions, func(p *config.Protocols) { p.ChatCompletions = false }, "glm-5.3", []string{"qwen3.8-max", "gpt-5.6-luna", "gemini-x"}},
		{RouteMessages, func(p *config.Protocols) { p.Messages = false }, "qwen3.8-max", []string{"glm-5.3", "gpt-5.6-luna", "gemini-x"}},
		{RouteResponses, func(p *config.Protocols) { p.Responses = false }, "gpt-5.6-luna", []string{"glm-5.3", "qwen3.8-max", "gemini-x"}},
		{RouteGemini, func(p *config.Protocols) { p.Gemini = false }, "gemini-x", []string{"glm-5.3", "qwen3.8-max", "gpt-5.6-luna"}},
	}
	for _, tc := range routes {
		t.Run(string(tc.route), func(t *testing.T) {
			cfg := testCfg()
			tc.disable(&cfg.Protocols)
			m := newManager(cfg, &fakeClient{resp: okDoc(chat("glm-5.3"), msgs("qwen3.8-max"), resp("gpt-5.6-luna"), gem("gemini-x"))})
			mustRefresh(t, m)

			models := m.Models()
			for _, mo := range models {
				if mo.UpstreamID == tc.excluded {
					t.Errorf("disabled-protocol model %q still routable: %+v", tc.excluded, mo)
				}
			}
			for _, id := range tc.routable {
				findModel(t, models, id)
			}
			want := []UnsupportedModel{{UpstreamID: tc.excluded, Reason: fmt.Sprintf("protocol %s disabled by config", tc.route)}}
			if got := m.Unsupported(); !reflect.DeepEqual(got, want) {
				t.Fatalf("unsupported = %+v, want %+v", got, want)
			}
		})
	}
}

func TestProtocolEnabledUnknownRouteDefaultsTrue(t *testing.T) {
	m := newManager(testCfg(), &fakeClient{})
	if !m.protocolEnabled(Route("bogus")) {
		t.Fatal("unknown routes must default to enabled")
	}
}

// TestSeedFromAppliesProtocolKillSwitch pins the seed-revalidation fix: a
// protocol disabled between snapshots must demote its routed records at
// seed time. Otherwise an operator flipping protocols.messages=false during
// a docs outage keeps serving messages-routed models off the stale snapshot
// until a refresh succeeds — the kill-switch is inert exactly when it needs
// to bite.
func TestSeedFromAppliesProtocolKillSwitch(t *testing.T) {
	prev := newManager(testCfg(), &fakeClient{resp: okDoc(msgs("minimax-m3"), chat("glm-5.3"))})
	mustRefresh(t, prev)

	cfg := testCfg()
	cfg.Protocols.Messages = false
	m := newManager(cfg, &fakeClient{})
	m.SeedFrom(prev)

	findModel(t, m.Models(), "glm-5.3") // unaffected route keeps serving
	if _, ok := m.Lookup("minimax-m3"); ok {
		t.Fatal("disabled-protocol record must not resolve via Lookup")
	}
	want := []UnsupportedModel{{UpstreamID: "minimax-m3", Reason: "protocol messages disabled by config"}}
	if got := m.Unsupported(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unsupported = %+v, want %+v", got, want)
	}
	findModel(t, prev.Models(), "minimax-m3") // seeding must not mutate prev
}

// TestSeedFromRecomputesPublicIDsUnderNewPrefix pins prefix recomputation at
// seed time: records rebuilt from prev's rows publish m's configured prefix
// value, and prev's prefixed public id stops resolving.
func TestSeedFromRecomputesPublicIDsUnderNewPrefix(t *testing.T) {
	prev := newManager(testCfg(), &fakeClient{resp: okDoc(chat("glm-5.3"))})
	mustRefresh(t, prev)

	cfg := testCfg()
	cfg.ModelPrefix.Value = "og"
	m := newManager(cfg, &fakeClient{})
	m.SeedFrom(prev)

	got := m.Models()
	if len(got) != 1 || got[0].PublicID != "og/glm-5.3" {
		t.Fatalf("seeded models = %+v, want glm-5.3 republished as og/glm-5.3", got)
	}
	if _, ok := m.Lookup("opencode-go/glm-5.3"); ok {
		t.Error("old-prefix public id must stop resolving after seed")
	}
}

// TestSwapPublicUpstreamCrossKeyCollision pins the index-collision fix: with
// the prefix enabled, upstream "foo" publishes public "opencode-go/foo" while
// an upstream literally named "opencode-go/foo" claims that same key as its
// UpstreamID; last-write-wins would silently misroute one of them. First in
// table order wins, a warning is emitted, the loser lands in Unsupported.
func TestSwapPublicUpstreamCrossKeyCollision(t *testing.T) {
	const wantMsg = `public id "opencode-go/foo" collides with upstream id "opencode-go/foo"`
	for _, order := range []string{"foo first", "opencode-go/foo first"} {
		t.Run(order, func(t *testing.T) {
			ids := []string{"foo", "opencode-go/foo"}
			if order == "opencode-go/foo first" {
				ids[0], ids[1] = ids[1], ids[0]
			}
			m := newManager(testCfg(), &fakeClient{resp: okDoc(chat(ids[0]), msgs(ids[1]))})
			mustRefresh(t, m)

			models := m.Models()
			if len(models) != 1 || models[0].UpstreamID != ids[0] || models[0].PublicID != "opencode-go/"+ids[0] {
				t.Fatalf("models = %+v, want single winner %q in table order", models, ids[0])
			}
			if rec, ok := m.Lookup("opencode-go/foo"); !ok || rec.UpstreamID != ids[0] {
				t.Fatalf("contested key resolves to %+v (%v), want the first-in-order record", rec, ok)
			}
			if got := m.Warnings(); !reflect.DeepEqual(got, []string{wantMsg}) {
				t.Fatalf("warnings = %v, want %q", got, wantMsg)
			}
			want := []UnsupportedModel{{UpstreamID: ids[1], Reason: wantMsg}}
			if got := m.Unsupported(); !reflect.DeepEqual(got, want) {
				t.Fatalf("unsupported = %+v, want %+v", got, want)
			}
		})
	}
}
