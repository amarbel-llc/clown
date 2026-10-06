// Package jugglerdecide implements the router half of `juggler decide`
// (FDR 0019 §1): one request to an OpenRouter Decisions-API-style endpoint,
// classification of the answer against a confidence threshold, and the
// decision stanza body that records the outcome in the run's room.
//
// The Decisions API (POST /api/alpha/decisions) is alpha and the types here
// come from its documentation only; no authenticated call has been made.
// Only `choice` questions are interpreted; other question types are carried
// through as raw JSON.
package jugglerdecide

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Exit codes of `juggler decide` (FDR 0019 §1 table).
const (
	ExitUsable         = 0
	ExitUsageConfig    = 1
	ExitNoChoice       = 2
	ExitRoomPostFailed = 3
	ExitBelowThreshold = 4
)

// DefaultMinConfidence is the router confidence threshold lever (FDR 0019).
const DefaultMinConfidence = 0.5

// DefaultPath is appended to a registry URL that does not already end in
// "/decisions".
const DefaultPath = "/api/alpha/decisions"

// maxRawBytes bounds the raw response body echoed into no-choice output.
const maxRawBytes = 4096

// Verdict is the classification of one decision attempt.
type Verdict string

const (
	Usable         Verdict = "usable"
	BelowThreshold Verdict = "below-threshold"
	NoChoice       Verdict = "no-choice"
)

// ExitCode maps a verdict to the decide exit code (post success assumed).
func (v Verdict) ExitCode() int {
	switch v {
	case Usable:
		return ExitUsable
	case BelowThreshold:
		return ExitBelowThreshold
	default:
		return ExitNoChoice
	}
}

// ErrConfig marks a usage/config error detected before any HTTP call
// (exit code 1).
var ErrConfig = errors.New("jugglerdecide: config error")

// Question is one entry of the request's "questions" map. Only the fields
// needed to validate a choice are interpreted; the rest rides in the raw
// questions JSON.
type Question struct {
	Type     string            `json:"type"`
	Criteria map[string]string `json:"criteria,omitempty"`
}

// Payload is the stdin document: the Decisions request minus model/auth.
type Payload struct {
	State     json.RawMessage `json:"state"`
	Questions json.RawMessage `json:"questions"`
}

// Request is the input to Decide.
type Request struct {
	HTTPClient *http.Client
	// Endpoint is the full Decisions URL (see EndpointFor).
	Endpoint string
	Token    string
	// Model is the upstream model id sent as "model".
	Model string
	// Payload is the raw stdin document {"state","questions"}.
	Payload []byte
	// MinConfidence is the inclusive usable threshold in [0,1].
	MinConfidence float64
	// Question is the question id to gate on. Empty means "the single
	// question"; with several questions it is a config error.
	Question string
}

// Usage is the response's usage block.
type Usage struct {
	Cost         float64 `json:"cost"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
}

// Answer is a parsed `choice` answer.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// Response is the Decisions API response. Answers stay raw so non-choice
// answer types survive untouched.
type Response struct {
	Answers  map[string]json.RawMessage `json:"answers"`
	ID       string                     `json:"id,omitempty"`
	Model    string                     `json:"model,omitempty"`
	Provider string                     `json:"provider,omitempty"`
	Usage    *Usage                     `json:"usage,omitempty"`
}

// Outcome is the single classified result of a decision attempt.
type Outcome struct {
	Verdict  Verdict
	Reason   string
	Question string
	Choice   string
	// Confidence is the API's `confidence`, used verbatim for gating. Observed
	// (two live calls, 2026-10-06, 2 options): it is the MARGIN over the
	// runner-up (0.63 vs 0.37 -> 0.26), NOT the winning probability. Unverified
	// for 3+ options. It is never recomputed from probabilities.
	Confidence float64
	// TopProbability is the chosen option's probability, shown alongside
	// Confidence; nil when the response carried no probability for it.
	TopProbability *float64
	Threshold      float64
	HTTPStatus     int
	// Raw is the (truncated) raw response body, kept for no-choice output.
	Raw string

	Model        string // model id sent
	StateDigest  string
	Questions    json.RawMessage
	Response     Response
	AnswersJSON  json.RawMessage // the response's full "answers" object
	haveResponse bool
}

// EndpointFor turns a registry URL into the Decisions endpoint.
func EndpointFor(registryURL string) string {
	u := strings.TrimRight(registryURL, "/")
	if strings.HasSuffix(u, "/decisions") {
		return u
	}
	return u + DefaultPath
}

// StateDigest is the sha256 hex of the canonical (re-marshalled, sorted-key,
// compact) state JSON.
func StateDigest(state json.RawMessage) (string, error) {
	var v any
	if len(bytes.TrimSpace(state)) == 0 {
		v = nil
	} else if err := json.Unmarshal(state, &v); err != nil {
		return "", fmt.Errorf("%w: state is not valid JSON: %v", ErrConfig, err)
	}
	canon, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// Decide sends the request and classifies the result. A non-nil error means
// a config/usage problem before any call (wraps ErrConfig); every runtime
// failure is a NoChoice Outcome instead.
func Decide(ctx context.Context, req Request) (Outcome, error) {
	var payload Payload
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return Outcome{}, fmt.Errorf("%w: stdin is not a JSON object: %v", ErrConfig, err)
	}
	if len(payload.Questions) == 0 {
		return Outcome{}, fmt.Errorf("%w: stdin has no \"questions\"", ErrConfig)
	}
	var questions map[string]json.RawMessage
	if err := json.Unmarshal(payload.Questions, &questions); err != nil || len(questions) == 0 {
		return Outcome{}, fmt.Errorf("%w: \"questions\" must be a non-empty object", ErrConfig)
	}
	qid, err := gateQuestion(req.Question, questions)
	if err != nil {
		return Outcome{}, err
	}
	var q Question
	if err := json.Unmarshal(questions[qid], &q); err != nil {
		return Outcome{}, fmt.Errorf("%w: question %q: %v", ErrConfig, qid, err)
	}
	if q.Type != "choice" {
		return Outcome{}, fmt.Errorf("%w: question %q has type %q; only choice questions are gated", ErrConfig, qid, q.Type)
	}
	digest, err := StateDigest(payload.State)
	if err != nil {
		return Outcome{}, err
	}

	out := Outcome{
		Question:    qid,
		Threshold:   req.MinConfidence,
		Model:       req.Model,
		StateDigest: digest,
		Questions:   payload.Questions,
	}

	body, err := json.Marshal(struct {
		Model     string          `json:"model"`
		State     json.RawMessage `json:"state"`
		Questions json.RawMessage `json:"questions"`
	}{req.Model, nonNull(payload.State), payload.Questions})
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: marshal request: %v", ErrConfig, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.Endpoint, bytes.NewReader(body))
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: build request: %v", ErrConfig, err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.Token)

	client := req.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return out.noChoice(fmt.Sprintf("request to %s failed: %v", req.Endpoint, err)), nil
	}
	defer resp.Body.Close()
	out.HTTPStatus = resp.StatusCode
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return out.noChoice(fmt.Sprintf("reading response body: %v", err)), nil
	}
	out.Raw = truncate(string(raw))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason := fmt.Sprintf("endpoint returned status %d", resp.StatusCode)
		if msg := apiErrorMessage(raw); msg != "" {
			reason += ": " + msg
		}
		return out.noChoice(reason), nil
	}

	var parsed Response
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return out.noChoice(fmt.Sprintf("unparseable response: %v", err)), nil
	}
	rawAnswer, ok := parsed.Answers[qid]
	if !ok {
		return out.noChoice(fmt.Sprintf("response has no answer for question %q", qid)), nil
	}
	var ans Answer
	if err := json.Unmarshal(rawAnswer, &ans); err != nil {
		return out.noChoice(fmt.Sprintf("answer for %q unparseable: %v", qid, err)), nil
	}
	out.Response = parsed
	out.haveResponse = true
	if all, ok := rawAnswersObject(raw); ok {
		out.AnswersJSON = all
	}
	out.Choice, out.Confidence = ans.Choice, ans.Confidence
	if p, ok := ans.Probabilities[ans.Choice]; ok {
		out.TopProbability = &p
	}
	if _, ok := q.Criteria[ans.Choice]; !ok {
		out.Verdict = NoChoice
		out.Reason = fmt.Sprintf("choice %q is not among question %q's criteria", ans.Choice, qid)
		return out, nil
	}
	if ans.Confidence < req.MinConfidence {
		out.Verdict = BelowThreshold
		out.Reason = fmt.Sprintf("confidence %.4g below threshold %.4g", ans.Confidence, req.MinConfidence)
		return out, nil
	}
	out.Verdict = Usable
	out.Reason = fmt.Sprintf("confidence %.4g >= threshold %.4g", ans.Confidence, req.MinConfidence)
	return out, nil
}

func (o Outcome) noChoice(reason string) Outcome {
	o.Verdict = NoChoice
	o.Reason = reason
	return o
}

// apiErrorMessage extracts error.message from an OpenRouter error body such
// as {"error":{"message":"User not found.","code":401}}; "" if absent.
func apiErrorMessage(body []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil {
		return ""
	}
	return env.Error.Message
}

func gateQuestion(want string, questions map[string]json.RawMessage) (string, error) {
	if want != "" {
		if _, ok := questions[want]; !ok {
			return "", fmt.Errorf("%w: --question %q is not among the request's questions", ErrConfig, want)
		}
		return want, nil
	}
	if len(questions) == 1 {
		for id := range questions {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: %d questions given; --question is required to pick the gating one", ErrConfig, len(questions))
}

func rawAnswersObject(body []byte) (json.RawMessage, bool) {
	var env struct {
		Answers json.RawMessage `json:"answers"`
	}
	if json.Unmarshal(body, &env) != nil || len(env.Answers) == 0 {
		return nil, false
	}
	return env.Answers, true
}

func nonNull(m json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(m)) == 0 {
		return json.RawMessage("null")
	}
	return m
}

func truncate(s string) string {
	if len(s) <= maxRawBytes {
		return s
	}
	return s[:maxRawBytes]
}

// Stanza is the decision stanza body posted to the run's room.
type Stanza struct {
	Schema      int             `json:"schema"`
	Type        string          `json:"type"`
	Parent      string          `json:"parent,omitempty"`
	Model       string          `json:"model"`
	StateDigest string          `json:"state_digest"`
	Questions   json.RawMessage `json:"questions"`
	Answers     json.RawMessage `json:"answers"`
	Threshold   float64         `json:"threshold"`
	// TopProbability: see Outcome.TopProbability.
	TopProbability *float64 `json:"top_probability,omitempty"`
	Verdict        Verdict  `json:"verdict"`
	Reason         string   `json:"reason"`
	Usage          *Usage   `json:"usage"`
}

// Stanza builds the decision stanza for this outcome. parent is the
// provenance parent (the recording stanza id) and may be empty.
func (o Outcome) Stanza(parent string) Stanza {
	answers := o.AnswersJSON
	if len(answers) == 0 {
		answers = json.RawMessage("null")
	}
	return Stanza{
		Schema:         1,
		Type:           "decision",
		Parent:         parent,
		Model:          o.Model,
		StateDigest:    o.StateDigest,
		Questions:      o.Questions,
		Answers:        answers,
		Threshold:      o.Threshold,
		TopProbability: o.TopProbability,
		Verdict:        o.Verdict,
		Reason:         o.Reason,
		Usage:          o.Response.Usage,
	}
}

// Marshal renders the stanza as compact JSON.
func (s Stanza) Marshal() ([]byte, error) {
	return marshalPlain(s)
}

// marshalPlain is json.Marshal without HTML escaping, so ">=" in a reason
// reads as written in the room.
func marshalPlain(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// StdoutJSON renders what `juggler decide` prints on stdout: the full
// answers plus id/model/usage when a response was parsed, else
// {reason, http_status?, raw?}. A below-threshold or unknown-option outcome
// that has a response prints the response form and additionally carries the
// reason under "reason".
func (o Outcome) StdoutJSON() ([]byte, error) {
	if o.haveResponse {
		return marshalPlain(struct {
			Answers json.RawMessage `json:"answers"`
			ID      string          `json:"id,omitempty"`
			Model   string          `json:"model,omitempty"`
			Usage   *Usage          `json:"usage,omitempty"`
			Reason  string          `json:"reason,omitempty"`
		}{o.AnswersJSON, o.Response.ID, o.Response.Model, o.Response.Usage, o.nonUsableReason()})
	}
	return marshalPlain(struct {
		Reason     string `json:"reason"`
		HTTPStatus int    `json:"http_status,omitempty"`
		Raw        string `json:"raw,omitempty"`
	}{o.Reason, o.HTTPStatus, o.Raw})
}

func (o Outcome) nonUsableReason() string {
	if o.Verdict == Usable {
		return ""
	}
	return o.Reason
}
