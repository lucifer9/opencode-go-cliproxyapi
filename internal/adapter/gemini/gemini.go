// Package gemini serves the Google Generative AI route (FR-004) by running
// CLIProxyAPI's built-in translators inside the plugin: requests translate
// from the client format to Gemini, and responses translate back. This
// package owns only what the translators leave to an executor: the
// per-model URL, auth header, upstream error classification, and Gemini 4
// long-decode continuation (finishReason CONTINUATION + continuationToken).
package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin" // registers CPA's built-in translators

	"opencode-go-cliproxyapi/internal/adapter/shared"
	"opencode-go-cliproxyapi/internal/catalog"
	"opencode-go-cliproxyapi/internal/errclass"
)

// EndpointPath labels the route in error messages.
var EndpointPath = catalog.RouteGemini.EndpointPath()

// finishContinuation marks a response cut at the per-request cap while
// output budget remains; the request must be resent with the token.
const finishContinuation = "CONTINUATION"

// AuthHeaders returns the Gemini API key header, the scheme @ai-sdk/google
// and CPA's own Gemini executor use.
func AuthHeaders(key string) http.Header {
	h := http.Header{}
	h.Set("x-goog-api-key", key)
	return h
}

// URL appends the generate method to the docs table's per-model URL
// (".../google/v1beta/models/{id}").
func URL(modelURL string, stream bool) string {
	if stream {
		return modelURL + ":streamGenerateContent?alt=sse"
	}
	return modelURL + ":generateContent"
}

// supported reports whether CPA registers every leg a client format needs:
// the request translator plus stream and non-stream response translators.
// CPA keys all three by (client format, upstream format); an unregistered
// response leg would pass Gemini JSON straight through to the client.
func supported(client sdktranslator.Format) bool {
	return sdktranslator.HasRequestTransformer(client, sdktranslator.FormatGemini) &&
		sdktranslator.HasStreamResponseTransformer(client, sdktranslator.FormatGemini) &&
		sdktranslator.HasNonStreamResponseTransformer(client, sdktranslator.FormatGemini)
}

// BuildRequest translates a client request into a Gemini generateContent
// body via CPA's translator for sourceFormat.
func BuildRequest(upstreamModel, sourceFormat string, sourceBody []byte, stream bool) ([]byte, *errclass.Error) {
	from := sdktranslator.FromString(sourceFormat)
	if !supported(from) {
		return nil, shared.UnsupportedFormat(sourceFormat, EndpointPath)
	}
	if !json.Valid(sourceBody) {
		return nil, errclass.Translation(fmt.Sprintf("malformed %s request JSON", sourceFormat))
	}
	out := sdktranslator.TranslateRequest(from, sdktranslator.FormatGemini, upstreamModel, sourceBody, stream)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil || obj == nil {
		return nil, errclass.Translation(fmt.Sprintf("%s request cannot be translated to %s", sourceFormat, EndpointPath))
	}
	return closePrefill(obj, out), nil
}

// continueTurn closes an assistant prefill. OpenCode Go rejects contents
// ending with a model turn ("Requests ending with a model turn are not
// supported") and ignores an empty user turn, the fix CPA's executor uses;
// a short non-empty user turn is accepted and the model continues the
// prefill (both verified against the live upstream).
var continueTurn = json.RawMessage(`{"role":"user","parts":[{"text":"Continue."}]}`)

// closePrefill appends continueTurn when contents ends with a model turn.
// A model turn carrying functionResponse parts is a tool exchange, not a
// prefill, and is left last. body is returned unchanged otherwise.
func closePrefill(obj map[string]json.RawMessage, body []byte) []byte {
	var contents []json.RawMessage
	if json.Unmarshal(obj["contents"], &contents) != nil || len(contents) == 0 {
		return body
	}
	var last struct {
		Role  string `json:"role"`
		Parts []struct {
			FunctionResponse json.RawMessage `json:"functionResponse"`
		} `json:"parts"`
	}
	if json.Unmarshal(contents[len(contents)-1], &last) != nil || last.Role != "model" {
		return body
	}
	for _, p := range last.Parts {
		if len(p.FunctionResponse) > 0 {
			return body
		}
	}
	obj["contents"], _ = json.Marshal(append(contents, continueTurn))
	out, _ := json.Marshal(obj) // values are valid JSON; cannot fail
	return out
}

// WithContinuation returns body with the top-level continuationToken that
// resumes a long decode.
func WithContinuation(body []byte, token string) ([]byte, *errclass.Error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, errclass.Translation("upstream request is not a JSON object")
	}
	obj["continuationToken"], _ = json.Marshal(token) // a string always marshals
	out, _ := json.Marshal(obj)                       // RawMessage values are valid JSON
	return out, nil
}

// response is a decoded GenerateContentResponse: raw keeps every top-level
// field (responseId, modelVersion, promptFeedback, ...) so re-encoding only
// changes what this package rewrites in candidates and usageMetadata.
type response struct {
	raw        map[string]json.RawMessage
	Candidates []map[string]json.RawMessage
	Usage      map[string]json.RawMessage
	Error      *apiError
}

func parseResponse(b []byte) (*response, error) {
	r := &response{}
	if err := json.Unmarshal(b, &r.raw); err != nil {
		return nil, err
	}
	if r.raw == nil {
		return nil, fmt.Errorf("response is JSON null")
	}
	if err := unmarshalIfPresent(r.raw["candidates"], &r.Candidates); err != nil {
		return nil, err
	}
	if err := unmarshalIfPresent(r.raw["usageMetadata"], &r.Usage); err != nil {
		return nil, err
	}
	if err := unmarshalIfPresent(r.raw["error"], &r.Error); err != nil {
		return nil, err
	}
	return r, nil
}

func unmarshalIfPresent(raw json.RawMessage, v any) error {
	if !shared.HasContent(raw) {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// encode writes the rewritable views back into the full object.
func (r *response) encode() []byte {
	if r.Candidates != nil {
		r.raw["candidates"], _ = json.Marshal(r.Candidates)
	}
	if r.Usage != nil {
		r.raw["usageMetadata"], _ = json.Marshal(r.Usage)
	}
	out, _ := json.Marshal(r.raw) // values are valid JSON; cannot fail
	return out
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// classify maps a Gemini error object onto the shared classes (FR-009).
func (e *apiError) classify() *errclass.Error {
	status := e.Code
	if status == 0 {
		status = statusFromGRPC(e.Status)
	}
	if status == 0 {
		return errclass.UpstreamFallback(e.Message)
	}
	return errclass.FromStatus(status, e.Message)
}

func statusFromGRPC(s string) int {
	switch s {
	case "INVALID_ARGUMENT", "FAILED_PRECONDITION", "OUT_OF_RANGE":
		return http.StatusBadRequest
	case "UNAUTHENTICATED":
		return http.StatusUnauthorized
	case "PERMISSION_DENIED":
		return http.StatusForbidden
	case "NOT_FOUND":
		return http.StatusNotFound
	case "RESOURCE_EXHAUSTED":
		return http.StatusTooManyRequests
	case "UNAVAILABLE":
		return http.StatusServiceUnavailable
	case "DEADLINE_EXCEEDED":
		return http.StatusGatewayTimeout
	case "INTERNAL", "UNKNOWN":
		return http.StatusInternalServerError
	}
	return 0
}

// takeContinuation strips a CONTINUATION finish from the first candidate
// and returns its token, so translators never see an intermediate finish.
func (r *response) takeContinuation() string {
	if len(r.Candidates) == 0 {
		return ""
	}
	c := r.Candidates[0]
	var finish string
	_ = json.Unmarshal(c["finishReason"], &finish)
	if finish != finishContinuation {
		return ""
	}
	var token string
	_ = json.Unmarshal(c["continuationToken"], &token)
	delete(c, "finishReason")
	delete(c, "continuationToken")
	return token
}

// usageTotals accumulates usageMetadata counters across continuation
// requests. Each request re-sends the prompt plus all output so far, so the
// sum matches what upstream bills (the @ai-sdk/google policy).
type usageTotals map[string]int64

// usageCounters are the integer usageMetadata fields that add up across
// requests; per-modality detail arrays are left as the last request sent.
var usageCounters = []string{
	"promptTokenCount", "candidatesTokenCount", "totalTokenCount",
	"thoughtsTokenCount", "cachedContentTokenCount", "toolUsePromptTokenCount",
}

// apply adds earlier requests' totals into usage in place.
func (t usageTotals) apply(usage map[string]json.RawMessage) {
	if usage == nil {
		return
	}
	for _, k := range usageCounters {
		if t[k] == 0 {
			continue
		}
		var v int64
		_ = json.Unmarshal(usage[k], &v)
		usage[k], _ = json.Marshal(v + t[k])
	}
}

// add folds one finished request's usage into the totals.
func (t usageTotals) add(usage map[string]json.RawMessage) {
	for _, k := range usageCounters {
		var v int64
		if json.Unmarshal(usage[k], &v) == nil {
			t[k] += v
		}
	}
}

// NonStreamCollector merges the responses of one long decode into a single
// GenerateContentResponse: parts concatenate in order, usage sums, and the
// last response supplies every other field.
type NonStreamCollector struct {
	parts  []json.RawMessage
	totals usageTotals
	last   *response
}

// NewNonStreamCollector returns an empty collector.
func NewNonStreamCollector() *NonStreamCollector {
	return &NonStreamCollector{totals: usageTotals{}}
}

// Add consumes one successful upstream body and returns the continuation
// token when the decode must resume ("" when it is complete). An error
// object in a 200 body is classified like an HTTP error.
func (c *NonStreamCollector) Add(body []byte) (string, *errclass.Error) {
	r, err := parseResponse(body)
	if err != nil {
		return "", errclass.Translation("malformed Gemini response JSON: " + err.Error())
	}
	if r.Error != nil {
		return "", r.Error.classify()
	}
	token := r.takeContinuation()
	if len(r.Candidates) > 0 {
		var content struct {
			Parts []json.RawMessage `json:"parts"`
		}
		_ = json.Unmarshal(r.Candidates[0]["content"], &content)
		c.parts = append(c.parts, content.Parts...)
	}
	if token != "" {
		c.totals.add(r.Usage)
	} else {
		c.totals.apply(r.Usage)
	}
	c.last = r
	return token, nil
}

// Body renders the merged response.
func (c *NonStreamCollector) Body() []byte {
	r := c.last
	if r == nil {
		return []byte(`{}`)
	}
	if len(r.Candidates) > 0 {
		var content map[string]json.RawMessage
		_ = json.Unmarshal(r.Candidates[0]["content"], &content)
		if content == nil {
			content = map[string]json.RawMessage{}
		}
		parts := c.parts
		if parts == nil {
			parts = []json.RawMessage{}
		}
		content["parts"], _ = json.Marshal(parts)
		r.Candidates[0]["content"], _ = json.Marshal(content)
	}
	return r.encode()
}

// ConvertNonStreamResponse translates a merged Gemini response into
// sourceFormat. clientBody is the request as the client sent it and
// upstreamBody the translated Gemini request; CPA's translators read both
// (for example to restore sanitized tool names).
func ConvertNonStreamResponse(sourceFormat, model string, clientBody, upstreamBody []byte, status int, body []byte) ([]byte, *errclass.Error) {
	if status >= 400 {
		return nil, shared.UpstreamStatusError(status, body)
	}
	to := sdktranslator.FromString(sourceFormat)
	if !supported(to) {
		return nil, shared.UnsupportedFormat(sourceFormat, EndpointPath)
	}
	var param any
	out := sdktranslator.TranslateNonStream(context.Background(), sdktranslator.FormatGemini, to, model, clientBody, upstreamBody, body, &param)
	if !json.Valid(out) {
		return nil, errclass.Translation(fmt.Sprintf("%s response cannot be translated to %s", EndpointPath, sourceFormat))
	}
	return out, nil
}

// StreamConverter translates a Gemini SSE stream (data-only frames, no
// terminal sentinel) into sourceFormat events through CPA's stream
// translator. A long decode spans several upstream streams; the executor
// feeds them all into one converter, which hides intermediate CONTINUATION
// finishes and sums usage so the client sees one response. Sequential use
// only.
type StreamConverter struct {
	framer       *shared.SSEFramer
	to           sdktranslator.Format
	model        string
	clientBody   []byte
	upstreamBody []byte
	param        any
	totals       usageTotals
	token        string // pending continuation token
	emitted      bool
	flushed      bool
}

// NewStreamConverter prepares stream translation for one request.
func NewStreamConverter(sourceFormat, model string, clientBody, upstreamBody []byte) *StreamConverter {
	return &StreamConverter{
		framer:       shared.NewSSEFramer(false),
		to:           sdktranslator.FromString(sourceFormat),
		model:        model,
		clientBody:   clientBody,
		upstreamBody: upstreamBody,
		totals:       usageTotals{},
	}
}

// Feed consumes one network chunk. Gemini streams carry no terminal event,
// so done is always false: the stream ends when upstream closes and the
// executor either resumes a continuation or calls Flush.
func (sc *StreamConverter) Feed(chunk []byte) (events [][]byte, done bool, eErr *errclass.Error) {
	sc.framer.Push(chunk)
	for {
		_, data, _, ok := sc.framer.Next()
		if !ok {
			return events, false, nil
		}
		if data == "" {
			continue
		}
		r, err := parseResponse([]byte(data))
		if err != nil {
			return events, false, errclass.Translation("malformed Gemini stream chunk: " + shared.RedactedSnippet(data))
		}
		if r.Error != nil {
			return events, false, r.Error.classify()
		}
		payload := []byte(data)
		if token := r.takeContinuation(); token != "" {
			sc.token = token
			sc.totals.apply(r.Usage)
			sc.totals = usageTotals{}
			sc.totals.add(r.Usage)
			payload = r.encode()
		} else if len(sc.totals) > 0 && r.Usage != nil {
			sc.totals.apply(r.Usage)
			payload = r.encode()
		}
		out := sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatGemini, sc.to, sc.model, sc.clientBody, sc.upstreamBody, payload, &sc.param)
		if len(out) > 0 {
			sc.emitted = true
		}
		events = append(events, out...)
	}
}

// ContinuationToken returns and clears the token of a long decode that
// must resume; "" when the last stream completed the response.
func (sc *StreamConverter) ContinuationToken() string {
	t := sc.token
	sc.token = ""
	return t
}

// Flush ends the client stream after upstream closed: CPA's translators
// emit their terminal events on the "[DONE]" sentinel, the same way CPA's
// own Gemini executor finishes a stream. One-shot, and silent when nothing
// was emitted.
func (sc *StreamConverter) Flush() [][]byte {
	if sc.flushed || !sc.emitted {
		return nil
	}
	sc.flushed = true
	return sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatGemini, sc.to, sc.model, sc.clientBody, sc.upstreamBody, []byte("[DONE]"), &sc.param)
}
