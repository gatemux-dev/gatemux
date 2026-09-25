package guardrails

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func policy(mode string, terms ...string) Policy {
	return Policy{Name: "terms", Type: "banned_terms", Mode: mode, Phase: "both", Terms: terms}
}

func TestValidation(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `[] []`, `[{"name":"x","type":"webhook","mode":"block","phase":"both","terms":["x"]}]`, `[{"name":"x","type":"banned_terms","mode":"redact","phase":"both","terms":[""]}]`, `[{"name":"x","type":"banned_terms","mode":"block","phase":"both","terms":["x"],"fail_open":true}]`} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	p := policy("redact", "secret")
	raw, _ := json.Marshal([]Policy{p})
	if _, err := Decode(raw); err != nil {
		t.Fatal(err)
	}
	ps := make([]Policy, 9)
	for i := range ps {
		ps[i] = p
	}
	raw, _ = json.Marshal(ps)
	if _, err := Decode(raw); err == nil {
		t.Fatal("unbounded policy count")
	}
}

func TestRedactsEveryMessageAndRawResponse(t *testing.T) {
	f := New([]Policy{policy("redact", "secret")}, "pre")
	b, err := Request([]byte(`{"model":"test","messages":[{"role":"system","content":"SECRET first"},{"role":"user","content":"second secret"}]}`), f)
	if err != nil || strings.Contains(strings.ToLower(string(b)), "secret") || strings.Count(string(b), "[REDACTED]") != 2 {
		t.Fatalf("%s %v", b, err)
	}
	f = New([]Policy{policy("redact", "secret")}, "post")
	b, err = Response([]byte(`{"model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"secret and SECRET"},"finish_reason":"stop"}],"usage":{"total_tokens":10}}`), f, false)
	if err != nil || strings.Contains(strings.ToLower(string(b)), "secret") {
		t.Fatalf("%s %v", b, err)
	}
}

func TestStreamingEveryBoundary(t *testing.T) {
	for _, text := range []string{"safe secret safe", "safe KELVIN safe", "safe ſecret safe", "safe 秘密 safe"} {
		for split := 0; split <= len([]rune(text)); split++ {
			runes := []rune(text)
			for _, mode := range []string{"block", "redact", "flag"} {
				f := New([]Policy{policy(mode, "secret", "kelvin", "秘密")}, "post")
				a, err := f.Push(string(runes[:split]), false)
				b := ""
				if err == nil {
					b, err = f.Push(string(runes[split:]), true)
				}
				if mode == "block" {
					if !errors.Is(err, ErrBlocked) {
						t.Fatalf("missed block %q @%d", text, split)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if mode == "flag" && a+b != text {
					t.Fatalf("flag mutated text %q", a+b)
				}
				if mode == "redact" && a+b != "safe [REDACTED] safe" {
					t.Fatalf("leak %q @%d: %q", text, split, a+b)
				}
			}
		}
	}
}

func TestBoundedLookbehind(t *testing.T) {
	f := New([]Policy{policy("redact", "secret")}, "post")
	for i := 0; i < 10000; i++ {
		if _, err := f.Push("safe", false); err != nil {
			t.Fatal(err)
		}
		if len(f.pending) > 20 {
			t.Fatal("growing suffix")
		}
	}
	if f.Complete() == nil {
		t.Fatal("missing finish not rejected")
	}
	if _, err := f.Push("", true); err != nil {
		t.Fatal(err)
	}
	if err := f.Complete(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Text(strings.Repeat("x", MaxPayload+1)); err == nil {
		t.Fatal("unbounded payload")
	}
}

func TestUnsupportedPayloadsFailClosed(t *testing.T) {
	for _, raw := range []string{
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"secret"}]}]}`,
		`{"model":"m","messages":[{"role":"tool","content":"secret"}]}`,
		`{"model":"m","messages":[{"role":"user","content":"safe","extra":"secret"}]}`,
		`{"model":"m","messages":[{"role":"user","content":"safe"}],"tools":[]}`,
		`{"model":"m","messages":[{"role":"user","content":"safe"}],"n":2}`,
	} {
		if _, err := Request([]byte(raw), New([]Policy{policy("block", "secret")}, "pre")); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("accepted %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"choices":[{"index":0,"delta":{"content":"safe","tool_calls":[]}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"safe","reasoning_content":"secret"}}]}`,
		`{"choices":[{"index":0,"delta":{"refusal":"secret"}}]}`,
		`{"choices":[{"index":1,"delta":{"content":"secret"}}]}`,
		`{"choices":[],"future_output":"secret"}`,
	} {
		if _, err := Response([]byte(raw), New([]Policy{policy("block", "secret")}, "post"), true); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("accepted %s: %v", raw, err)
		}
	}
}
