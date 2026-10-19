// Package catalog fetches the OpenCode Go docs page, reads its Endpoints
// table (model ID, upstream URL, AI SDK package), assigns routes, and serves
// the last good snapshot (FR-002, FR-003, FR-004, FR-010).
//
// The docs table is the only source of models and routes: a row whose AI SDK
// package has no adapter, or whose URL leaves the docs host, is excluded from
// the routable set and kept in diagnostics.
//
// No scheduling lives here; M3 wires the refresh cadence.
package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"opencode-go-cliproxyapi/internal/config"
)

// catalogBudgetFloor keeps an undersized max-response-bytes knob from
// freezing stale serving forever: the docs page can grow larger than typical
// completion bodies, so the fetch budget never drops below 16 MiB while
// max-response-bytes stays meaningful for completions elsewhere.
const catalogBudgetFloor = 16 << 20

// Route is the upstream protocol a model is served through (FR-004).
type Route string

const (
	RouteChatCompletions Route = "chat-completions"
	RouteMessages        Route = "messages"
	RouteResponses       Route = "responses"
	RouteGemini          Route = "gemini"
)

// routeEndpoints maps each route to its upstream endpoint path under the
// inference base. Adapters use it to label errors and tool policy; request
// URLs come from the docs table (ModelRecord.UpstreamURL).
var routeEndpoints = map[Route]string{
	RouteChatCompletions: "/openai/v1/chat/completions",
	RouteMessages:        "/anthropic/v1/messages",
	RouteResponses:       "/openai/v1/responses",
	RouteGemini:          "/google/v1beta/models",
}

// EndpointPath returns the route's upstream endpoint path, or "" for
// unknown routes.
func (r Route) EndpointPath() string {
	return routeEndpoints[r]
}

func routeFromString(s string) (Route, bool) {
	if _, ok := routeEndpoints[Route(s)]; ok {
		return Route(s), true
	}
	return "", false
}

// packageRoutes maps the docs table's AI SDK package to the route serving
// it, the same rule the OpenCode client uses to pick its SDK.
var packageRoutes = map[string]Route{
	"@ai-sdk/openai":            RouteResponses,
	"@ai-sdk/openai-compatible": RouteChatCompletions,
	"@ai-sdk/anthropic":         RouteMessages,
	"@ai-sdk/google":            RouteGemini,
}

// ModelRecord is one routable catalog entry (arch §7, FR-003). The docs
// table carries no context, modality, or thinking metadata, so none is
// published.
type ModelRecord struct {
	PublicID    string
	UpstreamID  string
	DisplayName string
	Protocol    Route
	UpstreamURL string
}

// UnsupportedModel is a discovered model excluded from the routable set,
// retained for diagnostics (FR-004, open questions §10).
type UnsupportedModel struct {
	UpstreamID string
	Reason     string
}

// HostClient issues HTTP requests through the host (pluginapi.HTTPDo
// semantics); M3's host callback satisfies it directly.
type HostClient interface {
	Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)
}

// Manager owns the catalog snapshot. All accessors are safe for
// concurrent use alongside Refresh.
type Manager struct {
	cfg    config.Config
	client HostClient

	mu sync.Mutex
	// raw retains the parsed docs rows behind the current snapshot so
	// SeedFrom can rebuild records against a NEW config (protocol flags,
	// docs URL, and prefix settings may all have changed since the snapshot
	// was built).
	raw    []docRow
	models []ModelRecord
	// index maps both PublicID and UpstreamID to their record so ID
	// resolution is O(1); rebuilt atomically with models on every swap.
	index map[string]ModelRecord
	unsup []UnsupportedModel
	warns []string
}

// New returns a Manager serving cfg through the host client.
func New(cfg config.Config, client HostClient) *Manager {
	return &Manager{cfg: cfg, client: client}
}

// SeedFrom republishes prev's last-good snapshot into m by REBUILDING every
// record from prev's raw docs rows through the same validation path a
// refresh applies, but against m's OWN cfg: the protocolEnabled gate
// re-applies, PublicIDs recompute under m's prefix, URLs re-check against
// m's docs host, and collisions/diagnostics regenerate (arch §5).
//
// m keeps its OWN cfg: subsequent Refresh calls use m's models-doc-url/
// protocols/prefix (F5: a failed-refresh reconfigure must adopt the new
// manager rather than keep ticking the old URL). Because seeding rebuilds
// instead of copying, a config change between snapshots takes effect at seed
// time — e.g. a protocol kill-switch flipped during a catalog outage demotes
// its routes immediately instead of serving stale routable records until the
// outage ends.
func (m *Manager) SeedFrom(prev *Manager) {
	prev.mu.Lock()
	raw := prev.raw
	prev.mu.Unlock()
	m.swap(raw)
}

// Refresh fetches the docs page and swaps the catalog snapshot. The page is
// public, so no credential is sent. On failure it returns a classified,
// key-free error; the previous snapshot keeps serving while
// catalog.stale-while-unavailable is enabled, otherwise the routable set is
// cleared until the next success (FR-002).
func (m *Manager) Refresh(ctx context.Context) error {
	if m.client == nil {
		// Only direct construction can produce a nil host client
		// (production always wires the bridge); fail loudly through the
		// classified path instead of panicking inside Do.
		return m.fail("host client unavailable")
	}
	req := pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     m.cfg.ModelsDocURL,
		Headers: http.Header{"Accept": []string{"text/html"}},
	}
	resp, err := m.client.Do(ctx, req)
	if err != nil {
		return m.fail("network error")
	}
	budget := max(m.cfg.MaxResponseBytes, catalogBudgetFloor)
	if int64(len(resp.Body)) > budget {
		return m.fail("response exceeds max-response-bytes")
	}
	if resp.StatusCode != http.StatusOK {
		return m.fail(fmt.Sprintf("http %d", resp.StatusCode))
	}
	rows, err := parseDocTable(resp.Body)
	if err != nil {
		return m.fail(err.Error())
	}
	m.swap(rows)
	return nil
}

// fail applies the stale policy and returns the classified error.
func (m *Manager) fail(category string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cfg.Catalog.StaleWhileUnavailable {
		// Clear the index too — Lookup must stop resolving IDs whose
		// records are gone (FR-002). Raw entries go with them: a cleared
		// snapshot has nothing to seed.
		m.raw, m.models, m.index, m.unsup, m.warns = nil, nil, nil, nil, nil
	}
	return fmt.Errorf("catalog refresh failed: %s", category)
}

// swap builds the new snapshot from docs rows and installs it atomically
// under the mutex (FR-010 dedup).
func (m *Manager) swap(rows []docRow) {
	models := make([]ModelRecord, 0, len(rows))
	index := make(map[string]ModelRecord, len(rows)*2)
	var unsup []UnsupportedModel
	var warns []string
	seen := make(map[string]bool, len(rows))
	docURL, docErr := url.Parse(m.cfg.ModelsDocURL)
	for _, r := range rows {
		if r.ID == "" {
			unsup = append(unsup, UnsupportedModel{Reason: "missing id"})
			continue
		}
		if seen[r.ID] {
			warns = append(warns, fmt.Sprintf("duplicate model %q ignored; keeping first occurrence", r.ID))
			continue
		}
		seen[r.ID] = true
		route, ok := packageRoutes[r.Package]
		if !ok {
			unsup = append(unsup, UnsupportedModel{
				UpstreamID: r.ID,
				Reason:     fmt.Sprintf("unsupported AI SDK package %q", r.Package),
			})
			continue
		}
		if !m.protocolEnabled(route) {
			// Flags apply at snapshot build; a stale snapshot kept per
			// FR-002 reflects the flags it was built with until the next
			// successful refresh.
			unsup = append(unsup, UnsupportedModel{
				UpstreamID: r.ID,
				Reason:     fmt.Sprintf("protocol %s disabled by config", route),
			})
			continue
		}
		if docErr != nil || !sameOrigin(docURL, r.Endpoint) {
			// API keys and prompts go to this URL, so a docs row may only
			// point back at the host serving the docs page (spec 05 §2
			// trust boundary).
			unsup = append(unsup, UnsupportedModel{
				UpstreamID: r.ID,
				Reason:     "endpoint is not on the docs host",
			})
			continue
		}
		display := r.Name
		if display == "" {
			display = r.ID
		}
		rec := ModelRecord{
			PublicID:    config.PublicID(m.cfg, r.ID),
			UpstreamID:  r.ID,
			DisplayName: display,
			Protocol:    route,
			UpstreamURL: r.Endpoint,
		}
		// With a prefix enabled, one record's PublicID can equal another
		// record's UpstreamID (upstream "foo" and "opencode-go/foo" both
		// claim index key "<prefix>/foo"); last-write-wins would silently
		// misroute. First in table order wins (same dedup rule as
		// duplicate IDs); the later record is excluded. Same-role clashes
		// are impossible — distinct upstream IDs and an injective public
		// mapping — so the slot owner always holds the key via its other
		// role, and a record's own public==upstream rewrite is not a clash.
		if other, ok := index[rec.PublicID]; ok {
			reason := fmt.Sprintf("public id %q collides with upstream id %q", rec.PublicID, other.UpstreamID)
			warns = append(warns, reason)
			unsup = append(unsup, UnsupportedModel{UpstreamID: r.ID, Reason: reason})
			continue
		}
		if other, ok := index[rec.UpstreamID]; ok {
			reason := fmt.Sprintf("public id %q collides with upstream id %q", other.PublicID, rec.UpstreamID)
			warns = append(warns, reason)
			unsup = append(unsup, UnsupportedModel{UpstreamID: r.ID, Reason: reason})
			continue
		}
		models = append(models, rec)
		index[rec.PublicID] = rec
		index[rec.UpstreamID] = rec
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.raw, m.models, m.index, m.unsup, m.warns = rows, models, index, unsup, warns
}

// protocolEnabled reports whether the resolved route's protocol flag is on
// (spec 04 §4); flags default to true.
func (m *Manager) protocolEnabled(r Route) bool {
	switch r {
	case RouteChatCompletions:
		return m.cfg.Protocols.ChatCompletions
	case RouteMessages:
		return m.cfg.Protocols.Messages
	case RouteResponses:
		return m.cfg.Protocols.Responses
	case RouteGemini:
		return m.cfg.Protocols.Gemini
	}
	return true
}

// sameOrigin reports whether endpoint parses to an absolute URL with doc's
// scheme and host, and carries no userinfo, query, or fragment. Any parse
// failure counts as foreign (fail closed).
func sameOrigin(doc *url.URL, endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme == doc.Scheme && strings.EqualFold(u.Host, doc.Host)
}

// Models returns the routable snapshot (FR-003 identity fields). The slice
// is a shallow copy; records are rebuilt fresh on every swap and consumers
// treat them as read-only, so nested slices/thinking are shared safely.
func (m *Manager) Models() []ModelRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ModelRecord, len(m.models))
	copy(out, m.models)
	return out
}

// Lookup resolves an ID to its routable record, accepting the public ID
// (with or without prefix) or the bare upstream ID (FR-003): the PublicID
// key wins over UpstreamID when both exist.
func (m *Manager) Lookup(id string) (ModelRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.index[id]
	return rec, ok
}

// Unsupported returns models excluded from the routable set with reasons.
func (m *Manager) Unsupported() []UnsupportedModel {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]UnsupportedModel, len(m.unsup))
	copy(out, m.unsup)
	return out
}

// Warnings returns dedup and override diagnostics.
func (m *Manager) Warnings() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.warns))
	copy(out, m.warns)
	return out
}
