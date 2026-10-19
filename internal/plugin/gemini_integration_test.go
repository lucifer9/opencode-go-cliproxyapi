package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const geminiTestModel = "gemini-4-argon-contributor"

// geminiCall records one request the mock Gemini upstream received.
type geminiCall struct {
	path, rawQuery, key, session string
	body                         map[string]any
}

// mockGemini serves a docs page listing one Gemini model on itself and
// scripted generateContent / streamGenerateContent responses; plan maps the
// 1-based hit number to the response body (non-stream) or SSE frames.
type mockGemini struct {
	mu    sync.Mutex
	srv   *httptest.Server
	calls []geminiCall
	plan  func(hit int, stream bool) (status int, body string)
}

func newMockGemini(t *testing.T) *mockGemini {
	t.Helper()
	g := &mockGemini{}
	mux := http.NewServeMux()
	mux.HandleFunc("/docs/go", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, docPage(g.srv.URL, gemModel(geminiTestModel)))
	})
	mux.HandleFunc("/google/v1beta/models/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		g.mu.Lock()
		g.calls = append(g.calls, geminiCall{
			path: r.URL.Path, rawQuery: r.URL.RawQuery,
			key: r.Header.Get("x-goog-api-key"), session: r.Header.Get("x-opencode-session"),
			body: body,
		})
		hit := len(g.calls)
		g.mu.Unlock()
		stream := strings.HasSuffix(r.URL.Path, ":streamGenerateContent")
		status, out := g.plan(hit, stream)
		if stream && status < 400 {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, out)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *mockGemini) recorded() []geminiCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]geminiCall(nil), g.calls...)
}

func newGeminiManager(t *testing.T, g *mockGemini) (*Manager, *fakeCaller) {
	t.Helper()
	f := &fakeCaller{responder: newForwardingCaller()}
	m := NewManager(NewHostBridge(f.call))
	t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
	resp := mustHandle(t, m, "plugin.register", lifecycleRequestBody(integrationYAML(g.srv.URL)))
	if env := decodeEnv(t, resp); !env.OK {
		t.Fatalf("register: %s", resp)
	}
	return m, f
}

// geminiResp renders one GenerateContentResponse.
func geminiResp(text, finish, token string, prompt, candidates int) string {
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}}}
	if finish != "" {
		cand["finishReason"] = finish
	}
	if token != "" {
		cand["continuationToken"] = token
	}
	b, _ := json.Marshal(map[string]any{
		"responseId": "resp-1", "modelVersion": geminiTestModel, "candidates": []any{cand},
		"usageMetadata": map[string]any{"promptTokenCount": prompt, "candidatesTokenCount": candidates, "totalTokenCount": prompt + candidates},
	})
	return string(b)
}

func sse(frames ...string) string {
	var b strings.Builder
	for _, f := range frames {
		fmt.Fprintf(&b, "data: %s\r\n\r\n", f)
	}
	return b.String()
}

var geminiClientBodies = map[string]string{
	"openai":          `{"model":"opencode-go/` + geminiTestModel + `","messages":[{"role":"user","content":"hi"}]}`,
	"claude":          `{"model":"opencode-go/` + geminiTestModel + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
	"openai-response": `{"model":"opencode-go/` + geminiTestModel + `","input":"hi"}`,
}

// Every client format reaches the Gemini route through the generate URL
// with the Gemini key header and session, and gets the text back.
func TestGeminiNonStreamRoundTrip(t *testing.T) {
	for format, body := range geminiClientBodies {
		t.Run(format, func(t *testing.T) {
			g := newMockGemini(t)
			g.plan = func(int, bool) (int, string) { return http.StatusOK, geminiResp("sunny", "STOP", "", 4, 2) }
			m, _ := newGeminiManager(t, g)
			env := mustExecute(t, m, "opencode-go/"+geminiTestModel, format, []byte(body))
			if !env.OK {
				t.Fatalf("envelope = %+v", env.Error)
			}
			var out pluginapi.ExecutorResponse
			_ = json.Unmarshal(env.Result, &out)
			if !strings.Contains(string(out.Payload), "sunny") {
				t.Fatalf("payload = %s", out.Payload)
			}
			calls := g.recorded()
			if len(calls) != 1 {
				t.Fatalf("upstream calls = %d", len(calls))
			}
			c := calls[0]
			if c.path != "/google/v1beta/models/"+geminiTestModel+":generateContent" || c.key != "sk-test-1" || c.session == "" {
				t.Fatalf("upstream call = %+v", c)
			}
			if _, ok := c.body["contents"]; !ok {
				t.Fatalf("upstream body is not a Gemini request: %v", c.body)
			}
		})
	}
}

// A long decode resends the request with the continuation token and the
// client receives one merged response with summed usage.
func TestGeminiNonStreamLongDecode(t *testing.T) {
	g := newMockGemini(t)
	g.plan = func(hit int, _ bool) (int, string) {
		if hit == 1 {
			return http.StatusOK, geminiResp("Hello", "CONTINUATION", "tok-1", 5, 3)
		}
		return http.StatusOK, geminiResp(" world", "STOP", "", 8, 2)
	}
	m, _ := newGeminiManager(t, g)
	env := mustExecute(t, m, "opencode-go/"+geminiTestModel, "openai", []byte(geminiClientBodies["openai"]))
	if !env.OK {
		t.Fatalf("envelope = %+v", env.Error)
	}
	var out pluginapi.ExecutorResponse
	_ = json.Unmarshal(env.Result, &out)
	var chat struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]int64 `json:"usage"`
	}
	if err := json.Unmarshal(out.Payload, &chat); err != nil || len(chat.Choices) != 1 {
		t.Fatalf("payload = %s", out.Payload)
	}
	if chat.Choices[0].Message.Content != "Hello world" || chat.Choices[0].FinishReason != "stop" {
		t.Fatalf("choice = %+v", chat.Choices[0])
	}
	if chat.Usage["prompt_tokens"] != 13 || chat.Usage["completion_tokens"] != 5 {
		t.Fatalf("usage = %v", chat.Usage)
	}
	calls := g.recorded()
	if len(calls) != 2 || calls[0].body["continuationToken"] != nil || calls[1].body["continuationToken"] != "tok-1" {
		t.Fatalf("continuation requests = %+v", calls)
	}
}

func TestGeminiUpstreamErrorStatus(t *testing.T) {
	g := newMockGemini(t)
	g.plan = func(int, bool) (int, string) {
		return http.StatusTooManyRequests, `{"error":{"code":429,"message":"slow down","status":"RESOURCE_EXHAUSTED"}}`
	}
	m, _ := newGeminiManager(t, g)
	env := mustExecute(t, m, "opencode-go/"+geminiTestModel, "openai", []byte(geminiClientBodies["openai"]))
	if env.OK || env.Error == nil || env.Error.Code != "rate_limit" {
		t.Fatalf("envelope = %+v", env.Error)
	}
}

func runGeminiStream(t *testing.T, m *Manager, format string) {
	t.Helper()
	resp, err := m.HandleCall("executor.execute_stream",
		execStreamReqBodyForKey("opencode-go/"+geminiTestModel, format, []byte(geminiClientBodies[format]), "down-1", "sk-test-1"))
	if err != nil {
		t.Fatalf("execute_stream: %v", err)
	}
	if env := decodeEnv(t, resp); !env.OK {
		t.Fatalf("envelope = %s", resp)
	}
	m.bridge.WaitForInFlight(5 * time.Second)
}

// Streaming uses the SSE generate URL, delivers text in every client format,
// and closes both streams cleanly.
func TestGeminiStreamRoundTrip(t *testing.T) {
	want := map[string]string{"openai": `"finish_reason":"stop"`, "claude": "message_stop", "openai-response": "response.completed"}
	for format := range geminiClientBodies {
		t.Run(format, func(t *testing.T) {
			g := newMockGemini(t)
			g.plan = func(int, bool) (int, string) {
				return http.StatusOK, sse(geminiResp("Hel", "", "", 0, 0), geminiResp("lo", "STOP", "", 4, 2))
			}
			m, f := newGeminiManager(t, g)
			runGeminiStream(t, m, format)
			calls := g.recorded()
			if len(calls) != 1 || !strings.HasSuffix(calls[0].path, ":streamGenerateContent") || calls[0].rawQuery != "alt=sse" {
				t.Fatalf("upstream calls = %+v", calls)
			}
			joined := strings.Join(emittedEvents(t, f), "")
			if !strings.Contains(joined, "Hel") || !strings.Contains(joined, "lo") || !strings.Contains(joined, want[format]) {
				t.Fatalf("events:\n%s", joined)
			}
			assertCleanStreamClose(t, f)
		})
	}
}

// A streamed long decode resumes on a second upstream stream; the client
// sees one stream with a single finish carrying summed usage.
func TestGeminiStreamLongDecode(t *testing.T) {
	g := newMockGemini(t)
	g.plan = func(hit int, _ bool) (int, string) {
		if hit == 1 {
			return http.StatusOK, sse(geminiResp("Hel", "", "", 0, 0), geminiResp("lo", "CONTINUATION", "tok-1", 5, 3))
		}
		return http.StatusOK, sse(geminiResp(" world", "STOP", "", 8, 2))
	}
	m, f := newGeminiManager(t, g)
	runGeminiStream(t, m, "openai")

	calls := g.recorded()
	if len(calls) != 2 || calls[1].body["continuationToken"] != "tok-1" {
		t.Fatalf("upstream calls = %+v", calls)
	}
	events := emittedEvents(t, f)
	joined := strings.Join(events, "")
	if strings.Count(joined, `"finish_reason":"stop"`) != 1 || !strings.Contains(joined, " world") {
		t.Fatalf("events:\n%s", joined)
	}
	last := events[len(events)-1]
	if !strings.Contains(last, `"prompt_tokens":13`) || !strings.Contains(last, `"completion_tokens":5`) {
		t.Fatalf("final usage not summed: %s", last)
	}
	if got := len(f.callsOf(pluginabi.MethodHostStreamClose)); got != 1 {
		t.Fatalf("downstream closes = %d", got)
	}
}

// The host hands executors the request in the declared input format as
// Payload; OriginalRequest stays the raw client body (here a Gemini-native
// request translated by the host). The plugin must build from Payload.
func TestExecutorBuildsFromHostPayload(t *testing.T) {
	g := newMockGemini(t)
	g.plan = func(int, bool) (int, string) { return http.StatusOK, geminiResp("ok", "STOP", "", 1, 1) }
	m, _ := newGeminiManager(t, g)
	native := `{"contents":[{"role":"user","parts":[{"text":"native-marker"}]}]}`
	translated := `{"model":"opencode-go/` + geminiTestModel + `","messages":[{"role":"user","content":"payload-marker"}]}`
	b, _ := json.Marshal(executorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		AuthProvider: ProviderID, AuthAttributes: map[string]string{"api_key": "sk-test-1"},
		Model: "opencode-go/" + geminiTestModel, SourceFormat: "openai",
		OriginalRequest: []byte(native), Payload: []byte(translated),
	}})
	resp, err := m.HandleCall("executor.execute", b)
	if err != nil {
		t.Fatal(err)
	}
	if env := decodeEnv(t, resp); !env.OK {
		t.Fatalf("envelope = %s", resp)
	}
	calls := g.recorded()
	sent, _ := json.Marshal(calls[0].body)
	if !strings.Contains(string(sent), "payload-marker") || strings.Contains(string(sent), "native-marker") {
		t.Fatalf("upstream body = %s", sent)
	}
}
