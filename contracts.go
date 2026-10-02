package agenstra

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type JSON = map[string]any

var callRefPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var fieldPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func CanonicalJSON(v any) ([]byte, error) {
	if err := finiteJSON(v); err != nil {
		return nil, err
	}
	prepared, err := canonicalPrepare(v)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(prepared)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var normalized any
	if err := dec.Decode(&normalized); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalized); err != nil {
		return nil, err
	}
	canonical := bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	return unescapeJSONSeparators(canonical), nil
}
func pythonFloat(v float64) (string, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", errors.New("JSON numbers must be finite")
	}
	if v == 0 {
		if math.Signbit(v) {
			return "-0.0", nil
		}
		return "0.0", nil
	}
	abs := math.Abs(v)
	if abs >= 1e-4 && abs < 1e16 {
		s := strconv.FormatFloat(v, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s, nil
	}
	s := strconv.FormatFloat(v, 'e', -1, 64)
	parts := strings.SplitN(s, "e", 2)
	exponent, _ := strconv.Atoi(parts[1])
	return fmt.Sprintf("%se%+03d", parts[0], exponent), nil
}
func canonicalPrepare(v any) (any, error) {
	switch x := v.(type) {
	case float64:
		s, e := pythonFloat(x)
		if e != nil {
			return nil, e
		}
		return json.Number(s), nil
	case float32:
		s, e := pythonFloat(float64(x))
		if e != nil {
			return nil, e
		}
		return json.Number(s), nil
	case json.Number:
		raw := string(x)
		if strings.ContainsAny(raw, ".eE") {
			n, e := x.Float64()
			if e != nil {
				return nil, e
			}
			s, e := pythonFloat(n)
			if e != nil {
				return nil, e
			}
			return json.Number(s), nil
		}
		return x, nil
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, item := range x {
			prepared, e := canonicalPrepare(item)
			if e != nil {
				return nil, e
			}
			m[k] = prepared
		}
		return m, nil
	}
	if v == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil
		}
		return canonicalPrepare(rv.Elem().Interface())
	}
	if marshaler, ok := v.(json.Marshaler); ok {
		raw, e := marshaler.MarshalJSON()
		if e != nil {
			return nil, e
		}
		var result any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if e := dec.Decode(&result); e != nil {
			return nil, e
		}
		return canonicalPrepare(result)
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		items := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			prepared, e := canonicalPrepare(rv.Index(i).Interface())
			if e != nil {
				return nil, e
			}
			items[i] = prepared
		}
		return items, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return v, nil
		}
		m := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			prepared, e := canonicalPrepare(iter.Value().Interface())
			if e != nil {
				return nil, e
			}
			m[iter.Key().String()] = prepared
		}
		return m, nil
	case reflect.Struct:
		raw, e := json.Marshal(v)
		if e != nil {
			return nil, e
		}
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if e := dec.Decode(&m); e != nil {
			return nil, e
		}
		rt := rv.Type()
		for i := 0; i < rt.NumField(); i++ {
			field := rt.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if tag == "-" {
				continue
			}
			if field.Anonymous && tag == "" {
				prepared, e := canonicalPrepare(rv.Field(i).Interface())
				if e != nil {
					return nil, e
				}
				if embedded, ok := prepared.(map[string]any); ok {
					for key, value := range embedded {
						if _, exists := m[key]; exists {
							m[key] = value
						}
					}
				}
				continue
			}
			if tag == "" {
				tag = field.Name
			}
			if _, exists := m[tag]; !exists {
				continue
			}
			prepared, e := canonicalPrepare(rv.Field(i).Interface())
			if e != nil {
				return nil, e
			}
			m[tag] = prepared
		}
		return m, nil
	default:
		return v, nil
	}
}
func unescapeJSONSeparators(raw []byte) []byte {
	var out bytes.Buffer
	for i := 0; i < len(raw); {
		if raw[i] != '\\' {
			out.WriteByte(raw[i])
			i++
			continue
		}
		start := i
		for i < len(raw) && raw[i] == '\\' {
			i++
		}
		count := i - start
		if count%2 == 1 && i+5 <= len(raw) && raw[i] == 'u' && (bytes.Equal(raw[i:i+5], []byte("u2028")) || bytes.Equal(raw[i:i+5], []byte("u2029"))) {
			out.Write(raw[start : i-1])
			if raw[i+4] == '8' {
				out.Write([]byte("\xe2\x80\xa8"))
			} else {
				out.Write([]byte("\xe2\x80\xa9"))
			}
			i += 5
			continue
		}
		out.Write(raw[start:i])
	}
	return out.Bytes()
}
func finiteJSON(v any) error {
	switch t := v.(type) {
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return errors.New("JSON numbers must be finite")
		}
	case float32:
		if math.IsNaN(float64(t)) || math.IsInf(float64(t), 0) {
			return errors.New("JSON numbers must be finite")
		}
	case map[string]any:
		for _, x := range t {
			if err := finiteJSON(x); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range t {
			if err := finiteJSON(x); err != nil {
				return err
			}
		}
	}
	return nil
}
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var coded interface{ Code() string }
	if errors.As(err, &coded) {
		return coded.Code()
	}
	return err.Error()
}

type Fact struct {
	SourcePackID     string     `json:"source_pack_id,omitempty"`
	SourceRelease    string     `json:"source_release,omitempty"`
	SourceSubject    string     `json:"source_subject,omitempty"`
	FactID           string     `json:"fact_id"`
	SourceCapability string     `json:"source_capability"`
	SourceVersion    string     `json:"source_version"`
	Value            JSON       `json:"value"`
	Quality          string     `json:"quality"`
	ObservedAt       time.Time  `json:"observed_at"`
	ReferenceScope   string     `json:"reference_scope"`
	ConnectionID     *string    `json:"connection_id"`
	ExpiresAt        *time.Time `json:"expires_at"`
}
type FactView struct {
	Fact
	OmittedPaths       [][]any `json:"omitted_paths"`
	ReferenceAvailable bool    `json:"reference_available"`
}
type Observation struct {
	CallRef          string  `json:"call_ref"`
	Capability       string  `json:"capability"`
	Status           string  `json:"status"`
	FactID           *string `json:"fact_id"`
	ErrorCode        *string `json:"error_code"`
	Arguments        JSON    `json:"arguments"`
	ArgumentsOmitted bool    `json:"arguments_omitted"`
}
type ContextPacket struct {
	OriginPackID         string            `json:"origin_pack_id,omitempty"`
	Schema               string            `json:"schema"`
	Instruction          string            `json:"instruction"`
	Capabilities         []JSON            `json:"capabilities"`
	Facts                []FactView        `json:"facts"`
	Observations         []Observation     `json:"observations"`
	RoundIndex           int               `json:"round_index"`
	RoundsRemaining      int               `json:"rounds_remaining"`
	ToolCallsRemaining   int               `json:"tool_calls_remaining"`
	Skills               []JSON            `json:"skills"`
	LoadedSkills         map[string]string `json:"loaded_skills"`
	InspectedCapability  JSON              `json:"inspected_capability"`
	InspectedFact        JSON              `json:"inspected_fact"`
	Followups            []string          `json:"followups"`
	RuntimeFeatures      []string          `json:"runtime_features"`
	ContextOmissions     []string          `json:"context_omissions"`
	Memories             []MemoryView      `json:"memories,omitempty"`
	ModelTokensRemaining int64             `json:"model_tokens_remaining,omitempty"`
	MaxModelOutputTokens int               `json:"max_model_output_tokens,omitempty"`
	Progress             *RunProgress      `json:"progress,omitempty"`
}
type ToolCall struct {
	CallRef    string `json:"call_ref"`
	Capability string `json:"capability"`
	Arguments  JSON   `json:"arguments"`
	Reason     string `json:"reason"`
}
type Decision struct {
	Schema         string            `json:"schema"`
	Kind           string            `json:"kind"`
	Calls          []ToolCall        `json:"calls,omitempty"`
	AnswerMarkdown string            `json:"answer_markdown,omitempty"`
	FactIDs        []string          `json:"fact_ids,omitempty"`
	Field          string            `json:"field,omitempty"`
	Prompt         string            `json:"prompt,omitempty"`
	Name           string            `json:"name,omitempty"`
	FactID         string            `json:"fact_id,omitempty"`
	Path           []any             `json:"path,omitempty"`
	ModelCall      *ModelCallMetrics `json:"-"`
}

type DecisionTooManyCallsError struct{}

func (DecisionTooManyCallsError) Error() string {
	return "model_decision_invalid: tool_batch.calls has at most 4 items"
}
func (DecisionTooManyCallsError) Code() string { return "model_decision_invalid" }
func (d Decision) Validate() error {
	if d.Schema != "" && d.Schema != "agenstra.decision.v1" {
		return errors.New("model_decision_invalid")
	}
	switch d.Kind {
	case "tool_batch":
		if len(d.Calls) > 4 {
			return DecisionTooManyCallsError{}
		}
		if len(d.Calls) < 1 {
			return errors.New("model_decision_invalid")
		}
		for _, c := range d.Calls {
			if !callRefPattern.MatchString(c.CallRef) || len(c.Capability) < 1 || len(c.Capability) > 330 || len(c.Reason) < 1 || len(c.Reason) > 500 || c.Arguments == nil {
				return errors.New("model_decision_invalid")
			}
		}
	case "final":
		if len(d.AnswerMarkdown) < 1 || len(d.AnswerMarkdown) > 30000 || len(d.FactIDs) > 50 {
			return errors.New("model_decision_invalid")
		}
		for _, id := range d.FactIDs {
			if !validUUID(id) {
				return errors.New("model_decision_invalid")
			}
		}
	case "request_input":
		if !fieldPattern.MatchString(d.Field) || len(d.Prompt) < 1 || len(d.Prompt) > 1000 {
			return errors.New("model_decision_invalid")
		}
	case "read_skill", "inspect_capability":
		if len(d.Name) < 1 || len(d.Name) > 330 {
			return errors.New("model_decision_invalid")
		}
	case "inspect_fact":
		if !validUUID(d.FactID) || len(d.Path) > 16 {
			return errors.New("model_decision_invalid")
		}
		for _, p := range d.Path {
			switch x := p.(type) {
			case string:
				_ = x
			case json.Number:
				i, err := x.Int64()
				if err != nil || i < 0 {
					return errors.New("model_decision_invalid")
				}
			case float64:
				if x < 0 || x != math.Trunc(x) {
					return errors.New("model_decision_invalid")
				}
			case int:
				if x < 0 {
					return errors.New("model_decision_invalid")
				}
			default:
				return errors.New("model_decision_invalid")
			}
		}
	default:
		return errors.New("model_decision_invalid")
	}
	return finiteJSON(d.Calls)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validUUID(s string) bool { return uuidPattern.MatchString(s) }
func strictDecision(raw []byte) (Decision, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Decision{}, errors.New("model_decision_invalid")
	}
	var d Decision
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&d); err != nil {
		return d, errors.New("model_decision_invalid")
	}
	if _, ok := obj["schema"]; !ok {
		d.Schema = "agenstra.decision.v1"
	}
	allowed := map[string]map[string]bool{"tool_batch": {"schema": true, "kind": true, "calls": true}, "final": {"schema": true, "kind": true, "answer_markdown": true, "fact_ids": true}, "request_input": {"schema": true, "kind": true, "field": true, "prompt": true}, "read_skill": {"schema": true, "kind": true, "name": true}, "inspect_capability": {"schema": true, "kind": true, "name": true}, "inspect_fact": {"schema": true, "kind": true, "fact_id": true, "path": true}}
	keys := allowed[d.Kind]
	if keys == nil {
		return d, errors.New("model_decision_invalid")
	}
	for k := range obj {
		if !keys[k] {
			return d, errors.New("model_decision_invalid")
		}
	}
	if d.Kind == "tool_batch" {
		var calls []map[string]json.RawMessage
		if err := json.Unmarshal(obj["calls"], &calls); err != nil {
			return d, errors.New("model_decision_invalid")
		}
		for _, c := range calls {
			for k := range c {
				if k != "call_ref" && k != "capability" && k != "arguments" && k != "reason" {
					return d, errors.New("model_decision_invalid")
				}
			}
		}
	}
	return d, d.Validate()
}

type OperationReceipt struct {
	OperationID   string           `json:"operation_id"`
	Binding       OperationBinding `json:"binding"`
	PollArguments JSON             `json:"poll_arguments"`
	NextPollAt    float64          `json:"next_poll_at"`
	Deadline      float64          `json:"deadline"`
	Polls         int              `json:"polls"`
}
type Invocation struct {
	InvocationID      string            `json:"invocation_id"`
	Call              ToolCall          `json:"call"`
	OriginalArguments JSON              `json:"original_arguments"`
	ArgumentsSHA256   string            `json:"arguments_sha256"`
	Status            string            `json:"status"`
	ApprovedUntil     *float64          `json:"approved_until"`
	ApprovalExpiresAt *float64          `json:"approval_expires_at"`
	ApprovedHash      *string           `json:"approved_hash"`
	Attempts          int               `json:"attempts"`
	ErrorCode         *string           `json:"error_code"`
	FactID            *string           `json:"fact_id"`
	Operation         *OperationReceipt `json:"operation"`
	PollInFlight      bool              `json:"poll_in_flight"`
}
type RuntimeState struct {
	SchemaVersion       int                `json:"schema_version"`
	RunID               string             `json:"run_id"`
	Instruction         string             `json:"instruction"`
	Status              string             `json:"status"`
	Facts               []Fact             `json:"facts"`
	Observations        []Observation      `json:"observations"`
	ModelObservations   []Observation      `json:"model_observations"`
	Decisions           []JSON             `json:"decisions"`
	UsedRefs            []string           `json:"used_refs"`
	Repeated            map[string]int     `json:"repeated"`
	LoadedSkills        []string           `json:"loaded_skills"`
	Followups           []string           `json:"followups"`
	InspectedCapability *string            `json:"inspected_capability"`
	InspectedFact       JSON               `json:"inspected_fact"`
	RoundsUsed          int                `json:"rounds_used"`
	ToolCallsUsed       int                `json:"tool_calls_used"`
	PollCallsUsed       int                `json:"poll_calls_used"`
	Pending             []Invocation       `json:"pending"`
	AnswerMarkdown      string             `json:"answer_markdown"`
	ErrorCode           *string            `json:"error_code"`
	InputField          *string            `json:"input_field"`
	InputPrompt         *string            `json:"input_prompt"`
	ModelCalls          []ModelCallMetrics `json:"model_calls,omitempty"`
	ModelUsage          ModelUsage         `json:"model_usage"`
	Progress            *ProgressTracker   `json:"progress_tracker,omitempty"`
}
type RunResult struct {
	Status         string             `json:"status"`
	AnswerMarkdown string             `json:"answer_markdown"`
	ErrorCode      *string            `json:"error_code"`
	InputField     *string            `json:"input_field"`
	InputPrompt    *string            `json:"input_prompt"`
	Facts          []Fact             `json:"facts"`
	Observations   []Observation      `json:"observations"`
	Decisions      []JSON             `json:"decisions"`
	ModelCalls     []ModelCallMetrics `json:"model_calls,omitempty"`
	ModelUsage     ModelUsage         `json:"model_usage"`
}

func strptr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
