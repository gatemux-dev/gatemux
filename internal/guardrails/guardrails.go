// Package guardrails implements bounded, fail-closed literal text policies.
// Protected chat deliberately accepts only the documented plain-text surface.
package guardrails

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxPolicies = 8 // per scope; team and alias policies are additive
const MaxPayload = 2 << 20

var ErrBlocked = errors.New("guardrail blocked content")
var ErrUnsupported = errors.New("guardrail supports plain-text chat only")

type Policy struct {
	Name  string   `json:"name"`
	Type  string   `json:"type"`
	Mode  string   `json:"mode"`
	Phase string   `json:"phase"`
	Terms []string `json:"terms"`
}

func Decode(raw []byte) ([]Policy, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' {
		return nil, errors.New("policies must be a JSON array")
	}
	if len(raw) > 64<<10 {
		return nil, errors.New("guardrail configuration exceeds 64 KiB")
	}
	var policies []Policy
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&policies); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing configuration data")
	}
	if len(policies) > MaxPolicies {
		return nil, errors.New("maximum eight policies per scope")
	}
	seen := map[string]bool{}
	for _, p := range policies {
		if err := p.Validate(); err != nil {
			return nil, err
		}
		if seen[p.Name] {
			return nil, errors.New("duplicate policy name")
		}
		seen[p.Name] = true
	}
	return policies, nil
}

var policyName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func (p Policy) Validate() error {
	if !policyName.MatchString(p.Name) {
		return errors.New("policy name must be 1–64 letters, numbers, dots, underscores or hyphens")
	}
	if p.Type != "banned_terms" {
		return errors.New("only banned_terms policies are supported")
	}
	if p.Mode != "block" && p.Mode != "redact" && p.Mode != "flag" {
		return errors.New("mode must be block, redact or flag")
	}
	if p.Phase != "pre" && p.Phase != "post" && p.Phase != "both" {
		return errors.New("phase must be pre, post or both")
	}
	if len(p.Terms) == 0 || len(p.Terms) > 32 {
		return errors.New("provide 1–32 literal terms")
	}
	for _, term := range p.Terms {
		if !utf8.ValidString(term) || strings.TrimSpace(term) == "" || len(term) > 128 {
			return errors.New("terms must be nonempty UTF-8, at most 128 bytes each")
		}
	}
	return nil
}

type Result struct{ Name, Phase, Decision string }
type rule struct {
	policy   Policy
	patterns []*regexp.Regexp
	result   string
}
type Filter struct {
	rules   []rule
	phase   string
	hold    int
	pending string
	Latency time.Duration
}

func New(policies []Policy, phase string) *Filter {
	f := &Filter{phase: phase}
	for _, p := range policies {
		if p.Phase != phase && p.Phase != "both" {
			continue
		}
		r := rule{policy: p, result: "not_evaluated"}
		for _, term := range p.Terms {
			r.patterns = append(r.patterns, regexp.MustCompile("(?i)"+regexp.QuoteMeta(term)))
			f.hold = max(f.hold, utf8.RuneCountInString(term)-1)
		}
		f.rules = append(f.rules, r)
	}
	return f
}

func (f *Filter) Results() []Result {
	out := make([]Result, 0, len(f.rules))
	for _, r := range f.rules {
		out = append(out, Result{r.policy.Name, f.phase, r.result})
	}
	return out
}

// Push retains only the suffix needed to detect terms crossing event boundaries.
// Redacted spans are not split. Matching is case-insensitive Unicode simple fold.
func (f *Filter) Push(text string, final bool) (string, error) {
	started := time.Now()
	defer func() { f.Latency += time.Since(started) }()
	if !utf8.ValidString(text) || len(text) > MaxPayload {
		return "", ErrUnsupported
	}
	s := f.pending + text
	cut := len(s)
	if !final {
		for n := 0; n < f.hold && cut > 0; n++ {
			_, size := utf8.DecodeLastRuneInString(s[:cut])
			cut -= size
		}
	}
	// One byte mask per bounded event, not a traffic-dependent match-index list.
	mask := make([]bool, len(s))
	for i := range f.rules {
		r := &f.rules[i]
		if r.result == "not_evaluated" {
			r.result = "allow"
		}
		for _, re := range r.patterns {
			for pos := 0; pos < len(s); {
				m := re.FindStringIndex(s[pos:])
				if m == nil {
					break
				}
				a, b := pos+m[0], pos+m[1]
				pos = b
				r.result = r.policy.Mode
				if r.policy.Mode == "block" {
					return "", ErrBlocked
				}
				if r.policy.Mode == "redact" {
					for j := a; j < b; j++ {
						mask[j] = true
					}
				}
			}
		}
	}
	// Hold the entire union of overlapping redactions at the output boundary.
	for cut > 0 && cut < len(s) && mask[cut-1] && mask[cut] {
		cut--
	}
	var out strings.Builder
	for i := 0; i < cut; {
		if mask[i] {
			out.WriteString("[REDACTED]")
			for i < cut && mask[i] {
				i++
			}
		} else {
			out.WriteByte(s[i])
			i++
		}
		if out.Len() > MaxPayload {
			return "", errors.New("guardrail redacted output exceeds byte limit")
		}
	}
	f.pending = strings.Clone(s[cut:])
	// Arbitrarily overlapping terms can otherwise hold an unlimited suffix.
	if len(f.pending) > 1024 {
		return "", errors.New("guardrail lookbehind capacity exceeded")
	}
	return out.String(), nil
}

func (f *Filter) Text(s string) (string, error) { return f.Push(s, true) }

func object(raw []byte, allowed string) (map[string]json.RawMessage, error) {
	if len(raw) > MaxPayload || !utf8.Valid(raw) {
		return nil, ErrUnsupported
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, ErrUnsupported
	}
	for key := range m {
		if !strings.Contains(" "+allowed+" ", " "+key+" ") {
			return nil, fmt.Errorf("%w: field %s", ErrUnsupported, key)
		}
	}
	return m, nil
}

// Request rewrites the raw envelope, not a typed projection (which could leave
// original content intact). Tools, multimodal inputs and extensions fail closed.
func Request(raw []byte, f *Filter) ([]byte, error) {
	m, err := object(raw, "model messages stream stream_options temperature max_tokens max_completion_tokens top_p stop n presence_penalty frequency_penalty seed user")
	if err != nil {
		return nil, err
	}
	if n, ok := m["n"]; ok && string(n) != "1" {
		return nil, ErrUnsupported
	}
	var msgs []json.RawMessage
	if json.Unmarshal(m["messages"], &msgs) != nil || len(msgs) == 0 {
		return nil, ErrUnsupported
	}
	for i, rawMsg := range msgs {
		msg, err := object(rawMsg, "role content")
		if err != nil {
			return nil, err
		}
		var role, content string
		if json.Unmarshal(msg["role"], &role) != nil || (role != "system" && role != "developer" && role != "user" && role != "assistant") || string(msg["content"]) == "null" || json.Unmarshal(msg["content"], &content) != nil {
			return nil, ErrUnsupported
		}
		content, err = f.Text(content)
		if err != nil {
			return nil, err
		}
		msg["content"], _ = json.Marshal(content)
		msgs[i], _ = json.Marshal(msg)
	}
	m["messages"], _ = json.Marshal(msgs)
	return json.Marshal(m)
}

// Response validates every content-bearing field. A streaming filter keeps its
// suffix across calls; finish_reason flushes it before the terminal event.
func Response(raw []byte, f *Filter, streaming bool) ([]byte, error) {
	m, err := object(raw, "id object created model choices usage system_fingerprint service_tier")
	if err != nil {
		return nil, err
	}
	var choices []json.RawMessage
	if json.Unmarshal(m["choices"], &choices) != nil || len(choices) > 1 {
		return nil, ErrUnsupported
	}
	for i, c := range choices {
		choice, err := object(c, "index message delta finish_reason logprobs")
		if err != nil {
			return nil, err
		}
		if v, ok := choice["index"]; ok && string(v) != "0" {
			return nil, ErrUnsupported
		}
		if v, ok := choice["logprobs"]; ok && string(v) != "null" {
			return nil, ErrUnsupported
		}
		field := "message"
		if streaming {
			field = "delta"
			if _, ok := choice["message"]; ok {
				return nil, ErrUnsupported
			}
		} else if _, ok := choice["delta"]; ok {
			return nil, ErrUnsupported
		}
		msg, err := object(choice[field], "role content refusal")
		if err != nil {
			return nil, err
		}
		if v, ok := msg["refusal"]; ok && string(v) != "null" {
			return nil, ErrUnsupported
		}
		content := ""
		if v, ok := msg["content"]; ok && string(v) != "null" {
			if json.Unmarshal(v, &content) != nil {
				return nil, ErrUnsupported
			}
		}
		final := !streaming
		if v, ok := choice["finish_reason"]; ok && string(v) != "null" {
			var reason string
			if json.Unmarshal(v, &reason) != nil || (reason != "stop" && reason != "length" && reason != "content_filter") {
				return nil, ErrUnsupported
			}
			final = true
		}
		content, err = f.Push(content, final)
		if err != nil {
			return nil, err
		}
		msg["content"], _ = json.Marshal(content)
		choice[field], _ = json.Marshal(msg)
		choices[i], _ = json.Marshal(choice)
	}
	m["choices"], _ = json.Marshal(choices)
	return json.Marshal(m)
}

func (f *Filter) Complete() error {
	if f.pending != "" {
		return errors.New("guardrail stream missing finish event")
	}
	return nil
}
