package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"opencode-go-cliproxyapi/internal/errclass"
)

const testModel = "gemini-4-argon-contributor"

func TestURLAndAuthHeaders(t *testing.T) {
	base := "https://opencode.ai/inference/go/google/v1beta/models/" + testModel
	if got := URL(base, false); got != base+":generateContent" {
		t.Errorf("non-stream URL = %q", got)
	}
	if got := URL(base, true); got != base+":streamGenerateContent?alt=sse" {
		t.Errorf("stream URL = %q", got)
	}
	h := AuthHeaders("k1")
	if h.Get("x-goog-api-key") != "k1" || h.Get("Authorization") != "" {
		t.Errorf("headers = %v", h)
	}
}

func TestBuildRequestRejects(t *testing.T) {
	if _, eErr := BuildRequest(testModel, "bogus", []byte(`{}`), false); eErr == nil || eErr.Class != errclass.ClassUnsupported {
		t.Errorf("unknown format = %v", eErr)
	}
	// CPA translators accept any bytes and emit a skeleton; malformed
	// client JSON must fail as a translation error instead.
	if _, eErr := BuildRequest(testModel, "openai", []byte(`{"messages":`), false); eErr == nil || eErr.Class != errclass.ClassTranslation {
		t.Errorf("malformed JSON = %v", eErr)
	}
}

// Every client format reaches Gemini, and the request carries what the
// upstream needs: tool schemas as parametersJsonSchema and the documented
// signature sentinel on historical function calls, which clients cannot
// round-trip.
func TestBuildRequestPerSourceFormat(t *testing.T) {
	cases := map[string]string{
		"openai": `{"messages":[{"role":"user","content":"hi"},` +
			`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"get","arguments":"{}"}}]},` +
			`{"role":"tool","tool_call_id":"c1","content":"ok"}],` +
			`"tools":[{"type":"function","function":{"name":"get","parameters":{"type":"object","properties":{}}}}]}`,
		"claude":          `{"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
		"openai-response": `{"input":"hi"}`,
	}
	for format, body := range cases {
		out, eErr := BuildRequest(testModel, format, []byte(body), false)
		if eErr != nil {
			t.Fatalf("%s: %v", format, eErr)
		}
		var req struct {
			Contents []struct {
				Role  string            `json:"role"`
				Parts []json.RawMessage `json:"parts"`
			} `json:"contents"`
		}
		if err := json.Unmarshal(out, &req); err != nil || len(req.Contents) == 0 || req.Contents[0].Role != "user" {
			t.Fatalf("%s: contents = %s", format, out)
		}
		if format == "openai" {
			s := string(out)
			if !strings.Contains(s, `"thoughtSignature":"skip_thought_signature_validator"`) || !strings.Contains(s, `"parametersJsonSchema"`) {
				t.Errorf("openai request misses sentinel or schema: %s", out)
			}
		}
	}
}

func TestWithContinuation(t *testing.T) {
	out, eErr := WithContinuation([]byte(`{"contents":[],"model":"m"}`), "tok-1")
	if eErr != nil {
		t.Fatal(eErr)
	}
	var obj map[string]any
	_ = json.Unmarshal(out, &obj)
	if obj["continuationToken"] != "tok-1" || obj["model"] != "m" {
		t.Errorf("body = %s", out)
	}
	if _, eErr := WithContinuation([]byte(`[]`), "t"); eErr == nil {
		t.Error("non-object body accepted")
	}
}

func geminiBody(id, text, finish, token string, prompt, candidates int) string {
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}}}
	if finish != "" {
		cand["finishReason"] = finish
	}
	if token != "" {
		cand["continuationToken"] = token
	}
	b, _ := json.Marshal(map[string]any{
		"responseId":   id,
		"modelVersion": "gemini-4",
		"candidates":   []any{cand},
		"usageMetadata": map[string]any{
			"promptTokenCount": prompt, "candidatesTokenCount": candidates, "totalTokenCount": prompt + candidates,
		},
	})
	return string(b)
}

func TestNonStreamCollectorMergesLongDecode(t *testing.T) {
	c := NewNonStreamCollector()
	token, eErr := c.Add([]byte(geminiBody("r1", "Hel", "CONTINUATION", "tok-1", 5, 3)))
	if eErr != nil || token != "tok-1" {
		t.Fatalf("first add = %q %v", token, eErr)
	}
	token, eErr = c.Add([]byte(geminiBody("r2", "lo", "STOP", "", 8, 2)))
	if eErr != nil || token != "" {
		t.Fatalf("second add = %q %v", token, eErr)
	}
	var merged struct {
		ResponseID   string `json:"responseId"`
		ModelVersion string `json:"modelVersion"`
		Candidates   []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason      string `json:"finishReason"`
			ContinuationToken string `json:"continuationToken"`
		} `json:"candidates"`
		Usage map[string]int64 `json:"usageMetadata"`
	}
	body := c.Body()
	if err := json.Unmarshal(body, &merged); err != nil {
		t.Fatal(err)
	}
	cand := merged.Candidates[0]
	if len(cand.Content.Parts) != 2 || cand.Content.Parts[0].Text != "Hel" || cand.Content.Parts[1].Text != "lo" {
		t.Errorf("parts = %+v", cand.Content.Parts)
	}
	if cand.FinishReason != "STOP" || cand.ContinuationToken != "" || merged.ResponseID != "r2" || merged.ModelVersion != "gemini-4" {
		t.Errorf("merged = %s", body)
	}
	if merged.Usage["promptTokenCount"] != 13 || merged.Usage["candidatesTokenCount"] != 5 || merged.Usage["totalTokenCount"] != 18 {
		t.Errorf("usage = %v", merged.Usage)
	}
}

func TestErrorObjectsClassified(t *testing.T) {
	cases := []struct {
		body string
		want errclass.Class
	}{
		{`{"error":{"code":429,"message":"slow down","status":"RESOURCE_EXHAUSTED"}}`, errclass.ClassRateLimit},
		{`{"error":{"message":"bad key","status":"UNAUTHENTICATED"}}`, errclass.ClassAuth},
		{`{"error":{"message":"odd"}}`, errclass.ClassUpstream},
	}
	for _, c := range cases {
		if _, eErr := NewNonStreamCollector().Add([]byte(c.body)); eErr == nil || eErr.Class != c.want {
			t.Errorf("non-stream %s = %v, want %s", c.body, eErr, c.want)
		}
		sc := NewStreamConverter("openai", testModel, []byte(`{}`), []byte(`{}`))
		if _, _, eErr := sc.Feed([]byte("data: " + c.body + "\n\n")); eErr == nil || eErr.Class != c.want {
			t.Errorf("stream %s = %v, want %s", c.body, eErr, c.want)
		}
	}
	if _, eErr := NewNonStreamCollector().Add([]byte(`{oops`)); eErr == nil || eErr.Class != errclass.ClassTranslation {
		t.Errorf("malformed body = %v", eErr)
	}
	sc := NewStreamConverter("openai", testModel, []byte(`{}`), []byte(`{}`))
	if _, _, eErr := sc.Feed([]byte("data: {oops\n\n")); eErr == nil || eErr.Class != errclass.ClassTranslation {
		t.Errorf("malformed chunk = %v", eErr)
	}
}

func TestConvertNonStreamResponse(t *testing.T) {
	body := []byte(geminiBody("r1", "Hello", "STOP", "", 5, 3))
	for _, format := range []string{"openai", "claude", "openai-response"} {
		out, eErr := ConvertNonStreamResponse(format, testModel, []byte(`{"input":"hi","messages":[]}`), []byte(`{}`), 200, body)
		if eErr != nil || !strings.Contains(string(out), "Hello") {
			t.Errorf("%s: %s %v", format, out, eErr)
		}
	}
	if _, eErr := ConvertNonStreamResponse("openai", testModel, nil, nil, 503, []byte("down")); eErr == nil || eErr.Class != errclass.ClassUpstream {
		t.Errorf("503 = %v", eErr)
	}
}

// chatUsage extracts the usage object from the last openai chunk carrying one.
func chatChunks(t *testing.T, events [][]byte) (finishes []string, usage map[string]int64) {
	t.Helper()
	for _, e := range events {
		var chunk struct {
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage map[string]int64 `json:"usage"`
		}
		if err := json.Unmarshal(e, &chunk); err != nil {
			t.Fatalf("openai event is not JSON: %q", e)
		}
		for _, c := range chunk.Choices {
			if c.FinishReason != nil {
				finishes = append(finishes, *c.FinishReason)
			}
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	return finishes, usage
}

// A long decode spans two upstream streams; the client sees one response:
// no finish before the last stream, and usage summed across requests.
func TestStreamConverterLongDecode(t *testing.T) {
	sc := NewStreamConverter("openai", testModel, []byte(`{"messages":[]}`), []byte(`{}`))
	first := "data: " + geminiBody("r1", "Hel", "", "", 0, 0) + "\n\n" +
		"data: " + geminiBody("r1", "lo", "CONTINUATION", "tok-1", 5, 3) + "\n\n"
	events, done, eErr := sc.Feed([]byte(first))
	if eErr != nil || done {
		t.Fatalf("first stream: done=%v err=%v", done, eErr)
	}
	if finishes, _ := chatChunks(t, events); len(finishes) != 0 {
		t.Fatalf("intermediate CONTINUATION leaked a finish: %v", finishes)
	}
	if tok := sc.ContinuationToken(); tok != "tok-1" {
		t.Fatalf("token = %q", tok)
	}
	if tok := sc.ContinuationToken(); tok != "" {
		t.Fatalf("token not cleared: %q", tok)
	}

	second := "data: " + geminiBody("r2", " world", "STOP", "", 8, 2) + "\n\n"
	events, _, eErr = sc.Feed([]byte(second))
	if eErr != nil {
		t.Fatal(eErr)
	}
	finishes, usage := chatChunks(t, events)
	if len(finishes) != 1 || finishes[0] != "stop" {
		t.Fatalf("finishes = %v", finishes)
	}
	if usage["prompt_tokens"] != 13 || usage["completion_tokens"] != 5 || usage["total_tokens"] != 18 {
		t.Fatalf("usage = %v", usage)
	}
	if tok := sc.ContinuationToken(); tok != "" {
		t.Fatalf("completed stream still asks to continue: %q", tok)
	}
}

func TestStreamConverterFlush(t *testing.T) {
	if got := NewStreamConverter("claude", testModel, []byte(`{}`), []byte(`{}`)).Flush(); got != nil {
		t.Errorf("flush without output = %q", got)
	}
	sc := NewStreamConverter("claude", testModel, []byte(`{"messages":[]}`), []byte(`{}`))
	if _, _, eErr := sc.Feed([]byte("data: " + geminiBody("r1", "hi", "STOP", "", 1, 1) + "\n\n")); eErr != nil {
		t.Fatal(eErr)
	}
	tail := sc.Flush()
	if len(tail) == 0 || !strings.Contains(string(tail[len(tail)-1]), "message_stop") {
		t.Fatalf("flush = %q, want message_stop", tail)
	}
	if again := sc.Flush(); again != nil {
		t.Errorf("second flush = %q", again)
	}
}

// An assistant prefill ends the history with a model turn, which upstream
// rejects; a non-empty user turn closes it. Other histories are untouched.
func TestBuildRequestClosesPrefill(t *testing.T) {
	type content struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}
	contents := func(body []byte) []content {
		var req struct {
			Contents []content `json:"contents"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		return req.Contents
	}
	out, eErr := BuildRequest(testModel, "claude", []byte(`{"max_tokens":10,"messages":[{"role":"user","content":"count"},{"role":"assistant","content":"One,"}]}`), false)
	if eErr != nil {
		t.Fatal(eErr)
	}
	got := contents(out)
	if len(got) != 3 || got[1].Role != "model" || got[2].Role != "user" || got[2].Parts[0].Text != "Continue." {
		t.Fatalf("prefill contents = %+v", got)
	}
	for _, body := range []string{
		`{"messages":[{"role":"user","content":"hi"}]}`,
		`{"messages":[{"role":"assistant","content":"earlier"},{"role":"user","content":"hi"}]}`,
	} {
		out, _ := BuildRequest(testModel, "openai", []byte(body), false)
		if got := contents(out); got[len(got)-1].Role != "user" || got[len(got)-1].Parts[0].Text != "hi" {
			t.Fatalf("history ending with the user changed: %+v", got)
		}
	}
}

// Gemini 3+ signs visible answer text; Claude clients must still receive it
// as text, not as a thinking block (CPA's translator filed it as thinking
// before SDK v8.0.33). Real thoughts stay thinking.
func TestClaudeSignedTextStaysText(t *testing.T) {
	body := `{"responseId":"r1","modelVersion":"gemini-4","candidates":[{"content":{"role":"model","parts":[` +
		`{"text":"plan","thought":true},{"text":"pong","thoughtSignature":"sig-1"}]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`
	out, eErr := ConvertNonStreamResponse("claude", testModel, []byte(`{"messages":[]}`), []byte(`{}`), 200, []byte(body))
	if eErr != nil {
		t.Fatal(eErr)
	}
	var msg struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatal(err)
	}
	var text, thinking string
	for _, b := range msg.Content {
		text += b.Text
		thinking += b.Thinking
	}
	if text != "pong" || thinking != "plan" {
		t.Fatalf("non-stream content = %s", out)
	}

	sc := NewStreamConverter("claude", testModel, []byte(`{"messages":[]}`), []byte(`{}`))
	events, _, eErr := sc.Feed([]byte("data: " + body + "\n\n"))
	if eErr != nil {
		t.Fatal(eErr)
	}
	joined := strings.Join(func() []string {
		var s []string
		for _, e := range events {
			s = append(s, string(e))
		}
		return s
	}(), "")
	if !strings.Contains(joined, `"text_delta","text":"pong"`) || strings.Contains(joined, `"thinking":"pong"`) {
		t.Fatalf("stream events:\n%s", joined)
	}
}
