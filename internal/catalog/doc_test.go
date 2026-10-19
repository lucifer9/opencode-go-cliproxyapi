package catalog

import (
	"errors"
	"reflect"
	"testing"
)

// realPageExcerpt is trimmed from https://opencode.ai/v2/docs/console/go
// (2026-10-19): a pricing table precedes the Endpoints table, which wraps
// URLs and packages in <code>.
const realPageExcerpt = `<!DOCTYPE html><html><body><main>
<table><thead><tr><th>Plan</th><th>Price</th><th>Included usage</th></tr></thead><tbody><tr><td><strong>Go</strong></td><td><strong>$10/month</strong></td><td>Lower-cost access to the models below</td></tr></tbody></table>
<h2 id="endpoints">Endpoints</h2><p>You can also access Go models through the following API endpoints.</p>
<div class="docs-table-scroll" role="region" aria-label="Go model endpoints" tabIndex="0">


<table><thead><tr><th>Model</th><th>Model ID</th><th>Endpoint</th><th>AI SDK Package</th></tr></thead><tbody><tr><td>Grok 4.7</td><td>grok-4.7</td><td><code>https://opencode.ai/inference/go/openai/v1/responses</code></td><td><code>@ai-sdk/openai</code></td></tr><tr><td>Claude Haiku 5.5</td><td>claude-haiku-5-5</td><td><code>https://opencode.ai/inference/go/anthropic/v1/messages</code></td><td><code>@ai-sdk/anthropic</code></td></tr><tr><td>Gemini 4 Argon Contributor</td><td>gemini-4-argon-contributor</td><td><code>https://opencode.ai/inference/go/google/v1beta/models/gemini-4-argon-contributor</code></td><td><code>@ai-sdk/google</code></td></tr><tr><td>GLM-5.3</td><td>glm-5.3</td><td><code>https://opencode.ai/inference/go/openai/v1/chat/completions</code></td><td><code>@ai-sdk/openai-compatible</code></td></tr></tbody></table></div><p>These AI SDK packages are for applications</p>
</main></body></html>`

func TestParseDocTableRealPageExcerpt(t *testing.T) {
	rows, err := parseDocTable([]byte(realPageExcerpt))
	if err != nil {
		t.Fatalf("parseDocTable: %v", err)
	}
	want := []docRow{
		{"Grok 4.7", "grok-4.7", "https://opencode.ai/inference/go/openai/v1/responses", "@ai-sdk/openai"},
		{"Claude Haiku 5.5", "claude-haiku-5-5", "https://opencode.ai/inference/go/anthropic/v1/messages", "@ai-sdk/anthropic"},
		{"Gemini 4 Argon Contributor", "gemini-4-argon-contributor", "https://opencode.ai/inference/go/google/v1beta/models/gemini-4-argon-contributor", "@ai-sdk/google"},
		{"GLM-5.3", "glm-5.3", "https://opencode.ai/inference/go/openai/v1/chat/completions", "@ai-sdk/openai-compatible"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %+v\nwant %+v", rows, want)
	}
}

// Columns are located by header text, so a reordered table still parses
// and short rows yield empty cells for swap to diagnose.
func TestParseDocTableHeaderOrderAndShortRows(t *testing.T) {
	page := `<table>
<tr><th>AI SDK Package</th><th>Endpoint</th><th>Model ID</th><th>Model</th></tr>
<tr><td><code>@ai-sdk/anthropic</code></td><td> <code>https://h/x</code> </td><td>m1</td><td>M <em>One</em></td></tr>
<tr><td>@ai-sdk/openai</td></tr>
</table>`
	rows, err := parseDocTable([]byte(page))
	if err != nil {
		t.Fatalf("parseDocTable: %v", err)
	}
	want := []docRow{
		{Name: "M One", ID: "m1", Endpoint: "https://h/x", Package: "@ai-sdk/anthropic"},
		{Package: "@ai-sdk/openai"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %+v\nwant %+v", rows, want)
	}
}

func TestParseDocTableShapeDrift(t *testing.T) {
	cases := []struct {
		name string
		page string
		want error
	}{
		{"no tables", `<html><body><p>moved</p></body></html>`, errDocTableMissing},
		{"only other tables", `<table><tr><th>Plan</th><th>Price</th></tr><tr><td>Go</td><td>$10</td></tr></table>`, errDocTableMissing},
		{"renamed column", `<table><tr><th>Model</th><th>ID</th><th>Endpoint</th><th>AI SDK Package</th></tr><tr><td>a</td><td>b</td><td>c</td><td>d</td></tr></table>`, errDocTableMissing},
		{"header only", `<table><tr><th>Model</th><th>Model ID</th><th>Endpoint</th><th>AI SDK Package</th></tr></table>`, errDocTableEmpty},
		{"empty body", ``, errDocTableMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseDocTable([]byte(c.page)); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}
