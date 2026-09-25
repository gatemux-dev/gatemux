package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/router"
	"github.com/gatemux-dev/gatemux/internal/store"
)

const maxResponseBody = 8 << 20

// Old stored responses remain addressable; ownership checks are unchanged.
var responseIDPattern = regexp.MustCompile(`^resp_(?:gatemux|aiport)_[a-f0-9]{32}$`)
var upstreamResponseIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

type responseRequest struct {
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input"`
	Instructions    string          `json:"instructions"`
	Stream          bool            `json:"stream"`
	Store           *bool           `json:"store"`
	Background      bool            `json:"background"`
	PreviousID      string          `json:"previous_response_id"`
	MaxOutputTokens *int            `json:"max_output_tokens"`
	User            string          `json:"user"`
	raw             map[string]json.RawMessage
}

func decodeResponseRequest(body []byte) (*responseRequest, error) {
	req := &responseRequest{}
	if err := json.Unmarshal(body, req); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &req.raw); err != nil {
		return nil, err
	}
	if req.Model == "" || strings.TrimSpace(req.Model) != req.Model {
		return nil, fmt.Errorf("model is required without surrounding whitespace")
	}
	if len(req.Input) == 0 || string(req.Input) == "null" {
		return nil, fmt.Errorf("input is required")
	}
	var input any
	if err := json.Unmarshal(req.Input, &input); err != nil {
		return nil, err
	}
	switch value := input.(type) {
	case string:
		if value == "" {
			return nil, fmt.Errorf("input must not be empty")
		}
	case []any:
		if len(value) == 0 {
			return nil, fmt.Errorf("input must not be empty")
		}
	default:
		return nil, fmt.Errorf("input must be text or an array of response items")
	}
	if req.MaxOutputTokens != nil && (*req.MaxOutputTokens <= 0 || *req.MaxOutputTokens > 1000000) {
		return nil, fmt.Errorf("max_output_tokens must be between 1 and 1000000")
	}
	if req.Background {
		return nil, fmt.Errorf("background Responses require asynchronous settlement and are not supported")
	}
	for _, field := range []string{"conversation", "prompt"} {
		if value := req.raw[field]; len(value) > 0 && string(value) != "null" {
			return nil, fmt.Errorf("%s references require resource ownership and are not supported; use inline input", field)
		}
	}
	if req.PreviousID != "" && !responseIDPattern.MatchString(req.PreviousID) {
		return nil, fmt.Errorf("previous_response_id must be a GateMux-owned response ID")
	}
	if err := validateResponseItems(input, 0); err != nil {
		return nil, err
	}
	if value := req.raw["tools"]; len(value) > 0 && string(value) != "null" {
		var tools []struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(value, &tools); err != nil {
			return nil, err
		}
		for _, tool := range tools {
			if tool.Type != "function" && tool.Type != "custom" {
				return nil, fmt.Errorf("hosted tool %q requires separate resource/cost policy; only client-executed function/custom tools are supported", tool.Type)
			}
		}
	}
	return req, nil
}

// Reject provider-side resource references that bypass the gateway's ownership
// table. Inline content and client-executed tool results remain lossless on wire.
func validateResponseItems(value any, depth int) error {
	if depth > 32 {
		return fmt.Errorf("response input nesting exceeds 32 levels")
	}
	switch v := value.(type) {
	case map[string]any:
		if v["type"] == "item_reference" {
			return fmt.Errorf("item_reference is unsupported; use inline content or an owned previous_response_id")
		}
		for key, item := range v {
			if key == "file_id" || key == "vector_store_ids" || key == "container_id" {
				return fmt.Errorf("provider resource references in input are unsupported")
			}
			if err := validateResponseItems(item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := validateResponseItems(item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (req *responseRequest) encoded(model, previous string) (json.RawMessage, error) {
	copy := make(map[string]json.RawMessage, len(req.raw))
	for key, value := range req.raw {
		copy[key] = value
	}
	copy["model"], _ = json.Marshal(model)
	if req.MaxOutputTokens == nil {
		copy["max_output_tokens"] = json.RawMessage("1024")
	}
	if previous != "" {
		copy["previous_response_id"], _ = json.Marshal(previous)
	}
	return json.Marshal(copy)
}

func (req *responseRequest) estimate(previous *store.ResponseBinding) (int, int, error) {
	// An approximate byte projection includes tool schemas/instructions, not only
	// input text. Actual usage is authoritative at settlement.
	raw, _ := json.Marshal(req.raw)
	prompt := (len(raw) + 3) / 4
	if previous != nil {
		if previous.TotalTokens > int64(math.MaxInt-prompt) {
			return 0, 0, fmt.Errorf("response history token estimate overflow")
		}
		prompt += int(previous.TotalTokens)
	}
	completion := 1024
	if req.MaxOutputTokens != nil {
		completion = *req.MaxOutputTokens
	}
	if prompt > math.MaxInt-completion {
		return 0, 0, fmt.Errorf("response history token estimate overflow")
	}
	return prompt, completion, nil
}

func responseOwner(r *http.Request) string {
	if key := auth.VirtualKeyFromContext(r.Context()); key != nil && key.ID > 0 {
		return fmt.Sprintf("key:%d", key.ID)
	}
	if user := auth.OwnerUserFromContext(r.Context()); user != nil && user.ID > 0 {
		return fmt.Sprintf("user:%d", user.ID)
	}
	if key := auth.VirtualKeyFromContext(r.Context()); key != nil && strings.HasPrefix(key.Name, "jwt:") {
		digest := sha256.Sum256([]byte(key.Name))
		return "jwt:" + hex.EncodeToString(digest[:])
	}
	return ""
}

func newResponseID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "resp_gatemux_" + hex.EncodeToString(value[:]), nil
}

func responseTargetFingerprint(_ *router.Registry, target *router.Resolved) string {
	return target.Identity
}

type responseEnvelope struct {
	ID     string                   `json:"id"`
	Object string                   `json:"object"`
	Status string                   `json:"status"`
	Model  string                   `json:"model"`
	Usage  *providers.ResponseUsage `json:"usage"`
}

func rewriteResponse(body json.RawMessage, binding *store.ResponseBinding) (json.RawMessage, *responseEnvelope, error) {
	var envelope responseEnvelope
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, nil, err
	}
	if envelope.Object != "response" || !upstreamResponseIDPattern.MatchString(envelope.ID) {
		return nil, nil, fmt.Errorf("invalid upstream Responses envelope")
	}
	if binding.UpstreamID != "" && binding.UpstreamID != envelope.ID {
		return nil, nil, fmt.Errorf("upstream response ID changed")
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, err
	}
	binding.UpstreamID = envelope.ID
	binding.Status = envelope.Status
	if envelope.Usage != nil {
		u := envelope.Usage.Canonical()
		if u.PromptTokens < 0 || u.CompletionTokens < 0 || u.PromptTokens > math.MaxInt-u.CompletionTokens {
			return nil, nil, fmt.Errorf("invalid response usage totals")
		}
		binding.TotalTokens = int64(u.PromptTokens + u.CompletionTokens)
	}
	raw["id"], _ = json.Marshal(binding.ID)
	raw["model"], _ = json.Marshal(binding.Alias)
	if binding.PreviousID == "" {
		raw["previous_response_id"] = json.RawMessage("null")
	} else {
		raw["previous_response_id"], _ = json.Marshal(binding.PreviousID)
	}
	encoded, err := json.Marshal(raw)
	return encoded, &envelope, err
}
