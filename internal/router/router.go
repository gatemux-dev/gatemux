package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/gatemux-dev/gatemux/internal/admission"
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/providers/anthropic"
	"github.com/gatemux-dev/gatemux/internal/providers/azureopenai"
	"github.com/gatemux-dev/gatemux/internal/providers/bedrock"
	"github.com/gatemux-dev/gatemux/internal/providers/cohere"
	"github.com/gatemux-dev/gatemux/internal/providers/fireworks"
	"github.com/gatemux-dev/gatemux/internal/providers/gemini"
	"github.com/gatemux-dev/gatemux/internal/providers/groq"
	"github.com/gatemux-dev/gatemux/internal/providers/mistral"
	"github.com/gatemux-dev/gatemux/internal/providers/openai"
	"github.com/gatemux-dev/gatemux/internal/providers/openaicompat"
	"github.com/gatemux-dev/gatemux/internal/providers/openrouter"
	"github.com/gatemux-dev/gatemux/internal/providers/together"
	"github.com/gatemux-dev/gatemux/internal/providers/vertex"
	"github.com/gatemux-dev/gatemux/internal/store"
)

var (
	ErrUnknownAlias        = errors.New("unknown model alias")
	ErrNoHealthyDeployment = errors.New("no healthy deployment")
	ErrDeploymentSaturated = errors.New("all eligible deployments are at concurrency capacity")
)

const (
	breakerFailureThreshold = 3
	breakerCooldown         = 30 * time.Second
)

type Resolved struct {
	Identity       string
	DeploymentName string
	ProviderType   string
	Provider       providers.Provider
	UpstreamModel  string
	Priority       int
	Weight         int
	BaseURL        string
	APIKey         string
}

// Deployment returns the deployment by name, or nil. Used by passthrough
// handlers (doc 0006) to look up capability flags.
func (r *Registry) Deployment(name string) *store.Deployment {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.deployments[name]
}

// Provider returns the constructed provider client for a deployment, or nil
// when the deployment is unknown or could not be built (for example a
// missing credential). Used by the admin connection test.
func (r *Registry) Provider(name string) providers.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers[name]
}

type DeploymentHealth struct {
	Name                string     `json:"name"`
	ProviderType        string     `json:"provider_type"`
	Enabled             bool       `json:"enabled"`
	Ready               bool       `json:"ready"`
	HasCredential       bool       `json:"has_credential"`
	Circuit             string     `json:"circuit"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	OpenUntil           *time.Time `json:"open_until,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	InFlight            int64      `json:"in_flight"`
	MaxParallelRequests int64      `json:"max_parallel_requests"`
}

// DeploymentConcurrency is a point-in-time view of a deployment's local
// concurrency limiter. Limit 0 means unlimited.
type DeploymentConcurrency struct {
	InFlight int64
	Limit    int64
}

// DeploymentPermit accounts for one active upstream operation. It must stay
// live until a streaming response body is completely consumed, not merely
// until the upstream headers arrive.
type DeploymentPermit struct {
	name   string
	permit *admission.CounterPermit
}

func (p *DeploymentPermit) Name() string {
	if p == nil {
		return ""
	}
	return p.name
}

func (p *DeploymentPermit) Release() {
	if p == nil {
		return
	}
	p.permit.Release()
}

type aliasTarget struct {
	DeploymentName string
	Priority       int
	Weight         int
}

type breakerState struct {
	ConsecutiveFailures int
	LastFailure         time.Time
	OpenUntil           time.Time
	LastError           string
}

// AliasMeta is the per-alias metadata the router exposes to the v1
// handler — cache settings + smart-routing strategy (doc 0007).
type AliasMeta struct {
	CacheEnabled    bool
	CacheTTLSeconds int
	Strategy        string
	StrategyOptions map[string]any
}

// ResolveContext is request-level routing signal data. Empty fields mean
// "no constraint"; the router falls back to alias-level defaults.
type ResolveContext struct {
	// Tags from the X-Gatemux-Tags header. Combined with the alias's
	// `tag_match` option ("any" / "all", default "any") to filter
	// candidates.
	Tags []string
	// Region from the X-Gatemux-Region header (or a configured client-IP
	// derivation). Filters to deployments whose `region` column matches.
	Region string
}

// pricingKey identifies a (provider_type, upstream_model) row in the
// pricing table — same shape as store.Pricing without the rate columns.
type pricingKey struct {
	provider string
	model    string
}

type Registry struct {
	store               *store.Store
	log                 *slog.Logger
	concurrencyPolicies concurrencyPolicies

	mu               sync.RWMutex
	aliases          map[string][]aliasTarget
	aliasMeta        map[string]AliasMeta
	deployments      map[string]*store.Deployment
	identities       map[string]string
	providers        map[string]providers.Provider
	pricing          map[pricingKey]int64 // input_per_million_cents per (provider, model)
	rr               map[string]int
	breakers         map[string]*breakerState
	deploymentLimits map[string]*admission.Counter
	// breakerStore mirrors breaker state across replicas. nil in
	// single-replica/dev mode; the in-memory map is then authoritative.
	breakerStore BreakerStore
}

// SetBreakerStore plugs a cross-replica breaker mirror into the
// registry. Calling with nil reverts to local-only behavior. Safe to
// call once at boot; not safe to swap mid-flight.
func (r *Registry) SetBreakerStore(store BreakerStore) {
	r.breakerStore = store
}

func New(s *store.Store, log *slog.Logger) (*Registry, error) {
	r := &Registry{
		store:            s,
		log:              log,
		aliases:          map[string][]aliasTarget{},
		aliasMeta:        map[string]AliasMeta{},
		deployments:      map[string]*store.Deployment{},
		providers:        map[string]providers.Provider{},
		pricing:          map[pricingKey]int64{},
		rr:               map[string]int{},
		breakers:         map[string]*breakerState{},
		deploymentLimits: map[string]*admission.Counter{},
	}
	if err := r.Refresh(context.Background()); err != nil {
		return nil, fmt.Errorf("initial registry load: %w", err)
	}
	return r, nil
}

func (r *Registry) Refresh(ctx context.Context) error {
	if err := r.RefreshConcurrencyPolicies(ctx); err != nil {
		return fmt.Errorf("load concurrency policies: %w", err)
	}
	deps, err := r.store.ListDeployments(ctx)
	if err != nil {
		return fmt.Errorf("load deployments: %w", err)
	}
	aliases, err := r.store.ListAliasesWithDeployments(ctx)
	if err != nil {
		return fmt.Errorf("load aliases: %w", err)
	}

	newDep := map[string]*store.Deployment{}
	newIdentities := map[string]string{}
	newProv := map[string]providers.Provider{}
	for _, d := range deps {
		newDep[d.Name] = d
		newIdentities[d.Name] = d.RoutingIdentity()
		prov, ready, reason := buildProvider(d)
		if !ready {
			if reason != "" {
				r.log.Warn("deployment skipped", "deployment", d.Name, "reason", reason)
			}
			continue
		}
		newProv[d.Name] = prov
	}

	newAliases := map[string][]aliasTarget{}
	newMeta := map[string]AliasMeta{}
	for _, a := range aliases {
		targets := make([]aliasTarget, 0, len(a.Targets))
		if len(a.Targets) > 0 {
			for _, t := range a.Targets {
				targets = append(targets, aliasTarget{
					DeploymentName: t.Deployment,
					Priority:       t.Priority,
					Weight:         t.Weight,
				})
			}
		} else {
			for i, name := range a.Deployments {
				targets = append(targets, aliasTarget{
					DeploymentName: name,
					Priority:       i,
					Weight:         1,
				})
			}
		}
		newAliases[a.Alias] = targets
		newMeta[a.Alias] = AliasMeta{
			CacheEnabled:    a.CacheEnabled,
			CacheTTLSeconds: a.CacheTTLSeconds,
			Strategy:        a.Strategy,
			StrategyOptions: a.StrategyOptions,
		}
	}

	// Pricing rows feed cost-based routing. We snapshot input_per_million
	// because that's the dominant cost lever for chat; output is closer
	// to a constant proportion across providers and noisier for ordering.
	newPricing := map[pricingKey]int64{}
	if pricingRows, err := r.store.ListCurrentPricing(ctx); err == nil {
		for _, p := range pricingRows {
			newPricing[pricingKey{provider: p.ProviderType, model: p.UpstreamModel}] = p.InputPerMillionCents
		}
	}

	r.mu.Lock()
	// Reuse counters across refreshes so a hot configuration update never
	// forgets work already in flight. Removed deployments keep their counter
	// only until the final old request releases it, which also makes an
	// immediate remove/re-add cycle safe.
	if r.deploymentLimits == nil {
		r.deploymentLimits = map[string]*admission.Counter{}
	}
	for name, dep := range newDep {
		limit := int64(0)
		if dep.MaxParallelRequests != nil && *dep.MaxParallelRequests > 0 {
			limit = int64(*dep.MaxParallelRequests)
		}
		counter := r.deploymentLimits[name]
		if counter == nil {
			counter = admission.NewCounter(limit)
			r.deploymentLimits[name] = counter
		} else {
			counter.SetLimit(limit)
		}
	}
	for name, counter := range r.deploymentLimits {
		if _, exists := newDep[name]; !exists && counter.Stats().InFlight == 0 {
			delete(r.deploymentLimits, name)
		}
	}
	r.deployments = newDep
	r.identities = newIdentities
	r.providers = newProv
	r.aliases = newAliases
	r.aliasMeta = newMeta
	r.pricing = newPricing
	r.mu.Unlock()
	return nil
}

// TryAcquireDeployment reserves local capacity for one deployment. It never
// waits: callers should immediately try another eligible deployment on
// saturation. Holding the registry read lock makes the decision atomic with
// respect to a registry refresh changing the configured limit.
func (r *Registry) TryAcquireDeployment(name string) (*DeploymentPermit, DeploymentConcurrency, bool) {
	if r == nil {
		return nil, DeploymentConcurrency{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.deploymentLimits == nil {
		return &DeploymentPermit{name: name}, DeploymentConcurrency{}, true
	}
	counter := r.deploymentLimits[name]
	if counter == nil {
		return nil, DeploymentConcurrency{}, false
	}
	permit, ok := counter.TryAcquire()
	stats := counter.Stats()
	state := DeploymentConcurrency{
		InFlight: stats.InFlight,
		Limit:    stats.Limit,
	}
	if !ok {
		return nil, state, false
	}
	return &DeploymentPermit{name: name, permit: permit}, state, true
}

func (r *Registry) DeploymentConcurrency(name string) DeploymentConcurrency {
	if r == nil {
		return DeploymentConcurrency{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	counter := r.deploymentLimits[name]
	if counter == nil {
		return DeploymentConcurrency{}
	}
	stats := counter.Stats()
	return DeploymentConcurrency{InFlight: stats.InFlight, Limit: stats.Limit}
}

// Meta returns the per-alias metadata used outside the routing decision
// itself (cache toggles, future strategy hints).
func (r *Registry) Meta(alias string) (AliasMeta, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.aliasMeta[alias]
	return m, ok
}

// effectiveCapabilities returns the capability set the router should use
// for routing decisions: whatever's stored on the deployment row (the
// admin's override) wins, otherwise we fall back to the provider type's
// declared defaults. This is what makes "this is an embedding-only
// deployment" actually keep chat traffic away.
func effectiveCapabilities(d *store.Deployment, p providers.Provider) providers.Capabilities {
	if d != nil && d.Capabilities != nil {
		responses := p.Capabilities().Responses && d.Capabilities.Chat
		if d.Capabilities.Responses != nil {
			responses = *d.Capabilities.Responses
		}
		return providers.Capabilities{
			Responses:  responses,
			Chat:       d.Capabilities.Chat,
			StreamChat: d.Capabilities.StreamChat,
			Embeddings: d.Capabilities.Embeddings,
		}
	}
	return p.Capabilities()
}

// DefaultCapabilities reads the adapter declaration without initializing cloud
// credentials or network clients. Used when patching one capability on a row
// that previously inherited its provider defaults.
func DefaultCapabilities(providerType string) providers.Capabilities {
	var p providers.Provider
	switch providerType {
	case "openai":
		p = &openai.Client{}
	case "azure_openai":
		p = &azureopenai.Client{}
	case "openai_compatible", "ollama", "vllm":
		p = &openaicompat.Client{}
	case "anthropic":
		p = &anthropic.Client{}
	case "mistral":
		p = &mistral.Client{}
	case "groq":
		p = &groq.Client{}
	case "together":
		p = &together.Client{}
	case "fireworks":
		p = &fireworks.Client{}
	case "openrouter":
		p = &openrouter.Client{}
	case "cohere":
		p = &cohere.Client{}
	case "bedrock":
		p = &bedrock.Client{}
	case "vertex":
		p = &vertex.Client{}
	case "gemini":
		p = &gemini.Client{}
	default:
		return providers.Capabilities{}
	}
	return p.Capabilities()
}

func buildProvider(d *store.Deployment) (providers.Provider, bool, string) {
	if !d.Enabled {
		return nil, false, "deployment disabled"
	}

	var baseURL string
	if d.BaseURL != nil {
		baseURL = *d.BaseURL
	}
	apiKey := ""
	if d.CredentialRef != "" {
		apiKey = os.Getenv(d.CredentialRef)
	}

	switch d.ProviderType {
	case "openai":
		if apiKey == "" {
			return nil, false, "credential env empty"
		}
		return openai.New(apiKey, baseURL), true, ""
	case "azure_openai":
		if apiKey == "" {
			return nil, false, "credential env empty"
		}
		return azureopenai.New(apiKey, baseURL), true, ""
	case "openai_compatible", "ollama", "vllm":
		return openaicompat.New(apiKey, baseURL), true, ""
	case "anthropic":
		if apiKey == "" {
			return nil, false, "credential env empty"
		}
		return anthropic.New(apiKey, baseURL), true, ""
	case "mistral":
		if apiKey == "" {
			return nil, false, "credential env empty"
		}
		return mistral.New(apiKey, baseURL), true, ""
	case "groq":
		if apiKey == "" {
			return nil, false, "credential env empty"
		}
		return groq.New(apiKey, baseURL), true, ""
	case "together":
		if apiKey == "" {
			return nil, false, "credential env empty"
		}
		return together.New(apiKey, baseURL), true, ""
	case "fireworks":
		if apiKey == "" {
			return nil, false, "credential env empty"
		}
		return fireworks.New(apiKey, baseURL), true, ""
	case "openrouter":
		if apiKey == "" {
			return nil, false, "credential env empty"
		}
		return openrouter.New(apiKey, baseURL), true, ""
	case "cohere":
		if apiKey == "" {
			return nil, false, "credential env empty"
		}
		return cohere.New(apiKey, baseURL), true, ""
	case "bedrock":
		region := ""
		if d.Region != nil {
			region = *d.Region
		}
		// AWS credentials are resolved through the default chain (env,
		// profile, IAM role) — operators don't need to set CredentialRef
		// on the deployment row when running on EC2/ECS/Lambda.
		client, err := bedrock.New(context.Background(), region)
		if err != nil {
			return nil, false, "bedrock client init failed: " + err.Error()
		}
		return client, true, ""
	case "vertex":
		region := ""
		if d.Region != nil {
			region = *d.Region
		}
		// CredentialRef holds the GCP project_id for vertex (auth itself
		// uses GOOGLE_APPLICATION_CREDENTIALS or attached identity, not
		// an env var we manage).
		projectID := d.CredentialRef
		client, err := vertex.New(context.Background(), region, projectID)
		if err != nil {
			return nil, false, "vertex client init failed: " + err.Error()
		}
		return client, true, ""
	case "gemini":
		if apiKey == "" {
			return nil, false, "credential env empty"
		}
		return gemini.New(apiKey, baseURL), true, ""
	default:
		return nil, false, "unsupported provider type"
	}
}

func (r *Registry) Resolve(alias string) (*Resolved, error) {
	return r.ResolveNextFor(alias, nil, "")
}

func (r *Registry) ResolveNext(alias string, tried map[string]bool) (*Resolved, error) {
	return r.ResolveNextFor(alias, tried, "")
}

func (r *Registry) ResolveNextFor(alias string, tried map[string]bool, capability providers.Capability) (*Resolved, error) {
	return r.ResolveWithContext(alias, tried, capability, ResolveContext{})
}

// ResolveWithContext is the smart-routing entrypoint: it accepts the
// per-request signal context (tags, region) and applies the alias's
// configured strategy on top of the existing priority/weight selection.
//
// Strategies layer on top of priority — we pick a healthy priority band
// first, then within that band the strategy filters and orders. That
// preserves "primary + fallback" semantics for aliases that have multiple
// priorities while still letting cost/latency/region/tag pickiness apply.
func (r *Registry) ResolveWithContext(alias string, tried map[string]bool, capability providers.Capability, rc ResolveContext) (*Resolved, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	targets, ok := r.aliases[alias]
	if !ok || len(targets) == 0 {
		return nil, ErrUnknownAlias
	}
	meta := r.aliasMeta[alias]

	priorities := orderedPriorities(targets)
	now := time.Now()
	for _, priority := range priorities {
		candidates := r.candidatesForPriorityLocked(targets, priority, tried, true, now, capability, meta, rc)
		if len(candidates) == 0 {
			continue
		}
		return r.pickLocked(alias, priority, candidates, meta), nil
	}
	for _, priority := range priorities {
		candidates := r.candidatesForPriorityLocked(targets, priority, tried, false, now, capability, meta, rc)
		if len(candidates) == 0 {
			continue
		}
		return r.pickLocked(alias, priority, candidates, meta), nil
	}

	return nil, ErrNoHealthyDeployment
}

func (r *Registry) candidatesForPriorityLocked(
	targets []aliasTarget,
	priority int,
	tried map[string]bool,
	healthyOnly bool,
	now time.Time,
	capability providers.Capability,
	meta AliasMeta,
	rc ResolveContext,
) []aliasTarget {
	strategies := strategyChain(meta)
	tagMatch := stringOption(meta.StrategyOptions, "tag_match", "any")
	regionFallback := stringOption(meta.StrategyOptions, "region_fallback", "any")

	out := make([]aliasTarget, 0, len(targets))
	for _, target := range targets {
		if target.Priority != priority {
			continue
		}
		if tried != nil && tried[target.DeploymentName] {
			continue
		}
		dep := r.deployments[target.DeploymentName]
		prov := r.providers[target.DeploymentName]
		if dep == nil || prov == nil || !dep.Enabled {
			continue
		}
		if capability != "" && !effectiveCapabilities(dep, prov).Supports(capability) {
			continue
		}
		if healthyOnly && r.isOpenLocked(target.DeploymentName, now) {
			continue
		}
		if hasStrategy(strategies, "tagged") && !tagFilter(dep, rc.Tags, tagMatch) {
			continue
		}
		if hasStrategy(strategies, "region") && rc.Region != "" {
			if dep.Region == nil || *dep.Region != rc.Region {
				if regionFallback == "block" {
					continue
				}
				// region_fallback=any → keep candidate, but mark with
				// a low priority bump (handled in pickLocked)
			}
		}
		out = append(out, target)
	}
	return out
}

func (r *Registry) pickLocked(alias string, priority int, candidates []aliasTarget, meta AliasMeta) *Resolved {
	strategies := strategyChain(meta)
	// Cost ordering: pick the deployment with the lowest pricing row.
	// Ties (or missing pricing rows) fall back to weighted round-robin
	// so we stay well-defined in a freshly-installed gateway.
	if hasStrategy(strategies, "cost") && len(candidates) > 1 {
		sortCandidatesByCostLocked(candidates, r.deployments, r.pricing)
		chosen := candidates[0]
		return r.resolvedLocked(chosen)
	}

	key := fmt.Sprintf("%s|%d", alias, priority)
	total := 0
	for _, candidate := range candidates {
		weight := candidate.Weight
		if weight <= 0 {
			weight = 1
		}
		total += weight
	}
	idx := 0
	if total > 0 {
		idx = r.rr[key] % total
		r.rr[key]++
	}
	offset := 0
	chosen := candidates[0]
	for _, candidate := range candidates {
		weight := candidate.Weight
		if weight <= 0 {
			weight = 1
		}
		offset += weight
		if idx < offset {
			chosen = candidate
			break
		}
	}
	return r.resolvedLocked(chosen)
}

func (r *Registry) resolvedLocked(chosen aliasTarget) *Resolved {
	dep := r.deployments[chosen.DeploymentName]
	baseURL := ""
	if dep.BaseURL != nil {
		baseURL = *dep.BaseURL
	}
	apiKey := ""
	if dep.CredentialRef != "" {
		apiKey = os.Getenv(dep.CredentialRef)
	}
	return &Resolved{
		Identity:       r.identities[dep.Name],
		DeploymentName: chosen.DeploymentName,
		ProviderType:   dep.ProviderType,
		Provider:       r.providers[chosen.DeploymentName],
		UpstreamModel:  dep.UpstreamModel,
		Priority:       chosen.Priority,
		Weight:         chosen.Weight,
		BaseURL:        baseURL,
		APIKey:         apiKey,
	}
}

// strategyChain returns the ordered list of strategies that apply to an
// alias. Single `strategy` value or `compose` list both supported.
func strategyChain(meta AliasMeta) []string {
	if meta.StrategyOptions != nil {
		if compose, ok := meta.StrategyOptions["compose"].([]any); ok {
			out := make([]string, 0, len(compose))
			for _, c := range compose {
				if s, ok := c.(string); ok {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	if meta.Strategy != "" && meta.Strategy != "priority" {
		return []string{meta.Strategy}
	}
	return nil
}

func hasStrategy(chain []string, name string) bool {
	for _, s := range chain {
		if s == name {
			return true
		}
	}
	return false
}

func stringOption(opts map[string]any, key, fallback string) string {
	if opts == nil {
		return fallback
	}
	if v, ok := opts[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

// tagFilter returns true when the deployment's tags satisfy the request
// tag set under the given match mode.
//   - mode "any"  → at least one request tag is present on the deployment
//   - mode "all"  → every request tag is present on the deployment
//
// Empty request tags ⇒ no filter (all candidates pass).
func tagFilter(dep *store.Deployment, requestTags []string, mode string) bool {
	if len(requestTags) == 0 {
		return true
	}
	depTags := map[string]bool{}
	for _, t := range dep.Tags {
		depTags[t] = true
	}
	if mode == "all" {
		for _, t := range requestTags {
			if !depTags[t] {
				return false
			}
		}
		return true
	}
	// "any" (default)
	for _, t := range requestTags {
		if depTags[t] {
			return true
		}
	}
	return false
}

// sortCandidatesByCostLocked orders candidates ascending by their pricing
// row's input_per_million_cents. Deployments with no pricing row sort
// last (effectively cost=∞) so the cost strategy doesn't silently route
// to a deployment whose cost is unknown.
func sortCandidatesByCostLocked(
	candidates []aliasTarget,
	deps map[string]*store.Deployment,
	pricing map[pricingKey]int64,
) {
	sort.SliceStable(candidates, func(i, j int) bool {
		ci := candidateCost(candidates[i], deps, pricing)
		cj := candidateCost(candidates[j], deps, pricing)
		return ci < cj
	})
}

func candidateCost(t aliasTarget, deps map[string]*store.Deployment, pricing map[pricingKey]int64) int64 {
	dep := deps[t.DeploymentName]
	if dep == nil {
		return int64(1 << 62)
	}
	if cost, ok := pricing[pricingKey{provider: dep.ProviderType, model: dep.UpstreamModel}]; ok {
		return cost
	}
	return int64(1 << 62)
}

func (r *Registry) RecordSuccess(deployment string) {
	r.mu.Lock()
	state := r.ensureBreakerLocked(deployment)
	state.ConsecutiveFailures = 0
	state.OpenUntil = time.Time{}
	state.LastError = ""
	r.mu.Unlock()
	// Mirror to the cross-replica store so a peer's open-state clears
	// quickly. Failure here is non-fatal — local view stays correct.
	if r.breakerStore != nil {
		_ = r.breakerStore.RecordSuccess(context.Background(), deployment)
	}
}

func (r *Registry) RecordFailure(deployment string, err error) {
	if !providers.IsRetryable(err) {
		return
	}
	r.mu.Lock()
	state := r.ensureBreakerLocked(deployment)
	now := time.Now()
	if state.LastFailure.IsZero() || now.Sub(state.LastFailure) > breakerCooldown {
		state.ConsecutiveFailures = 0
	}
	state.ConsecutiveFailures++
	state.LastFailure = now
	state.LastError = err.Error()
	if state.ConsecutiveFailures >= breakerFailureThreshold {
		state.OpenUntil = now.Add(breakerCooldown)
	}
	r.mu.Unlock()
	if r.breakerStore != nil {
		// Pass the threshold so the store's INCR can decide to set
		// open_until at the same boundary the local logic uses.
		failures, openUntil, err := r.breakerStore.RecordFailure(context.Background(), deployment, breakerFailureThreshold, breakerCooldown)
		if err == nil && !openUntil.IsZero() {
			r.mu.Lock()
			ls := r.ensureBreakerLocked(deployment)
			if ls.OpenUntil.IsZero() || openUntil.After(ls.OpenUntil) {
				ls.OpenUntil = openUntil
			}
			if failures > ls.ConsecutiveFailures {
				ls.ConsecutiveFailures = failures
			}
			r.mu.Unlock()
		}
	}
}

func (r *Registry) Health() []DeploymentHealth {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.deployments))
	for name := range r.deployments {
		names = append(names, name)
	}
	sort.Strings(names)

	now := time.Now()
	out := make([]DeploymentHealth, 0, len(names))
	for _, name := range names {
		dep := r.deployments[name]
		state := r.breakers[name]
		hasCredential := dep.CredentialRef == "" || os.Getenv(dep.CredentialRef) != ""
		health := DeploymentHealth{
			Name:          name,
			ProviderType:  dep.ProviderType,
			Enabled:       dep.Enabled,
			Ready:         r.providers[name] != nil,
			HasCredential: hasCredential,
			Circuit:       "closed",
		}
		if counter := r.deploymentLimits[name]; counter != nil {
			stats := counter.Stats()
			health.InFlight = stats.InFlight
			health.MaxParallelRequests = stats.Limit
		}
		if state != nil {
			health.ConsecutiveFailures = state.ConsecutiveFailures
			health.LastError = state.LastError
			if !state.OpenUntil.IsZero() && state.OpenUntil.After(now) {
				openUntil := state.OpenUntil
				health.OpenUntil = &openUntil
				health.Circuit = "open"
			}
		}
		if !health.Ready && dep.Enabled {
			health.Circuit = "unavailable"
		}
		out = append(out, health)
	}
	return out
}

func (r *Registry) Aliases() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.aliases))
	for a := range r.aliases {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func (r *Registry) Targets(alias string) ([]*Resolved, error) {
	return r.TargetsFor(alias, "")
}

func (r *Registry) TargetsFor(alias string, capability providers.Capability) ([]*Resolved, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	targets, ok := r.aliases[alias]
	if !ok || len(targets) == 0 {
		return nil, ErrUnknownAlias
	}
	out := make([]*Resolved, 0, len(targets))
	for _, target := range targets {
		dep := r.deployments[target.DeploymentName]
		prov := r.providers[target.DeploymentName]
		if dep == nil || prov == nil || !dep.Enabled {
			continue
		}
		if capability != "" && !effectiveCapabilities(dep, prov).Supports(capability) {
			continue
		}
		out = append(out, &Resolved{
			Identity:       r.identities[dep.Name],
			DeploymentName: target.DeploymentName,
			ProviderType:   dep.ProviderType,
			Provider:       prov,
			UpstreamModel:  dep.UpstreamModel,
			Priority:       target.Priority,
			Weight:         target.Weight,
		})
	}
	if len(out) == 0 {
		return nil, ErrNoHealthyDeployment
	}
	return out, nil
}

func (r *Registry) ensureBreakerLocked(name string) *breakerState {
	state := r.breakers[name]
	if state == nil {
		state = &breakerState{}
		r.breakers[name] = state
	}
	return state
}

func (r *Registry) isOpenLocked(name string, now time.Time) bool {
	state := r.breakers[name]
	if state != nil && !state.OpenUntil.IsZero() && state.OpenUntil.After(now) {
		return true
	}
	// Cross-replica check. The IO timeout in BreakerStore caps this so
	// a slow Redis can't slow down request resolution.
	if r.breakerStore != nil {
		if openUntil, err := r.breakerStore.IsOpen(context.Background(), name); err == nil && !openUntil.IsZero() && openUntil.After(now) {
			// Mirror Redis's view into the local cache so the next call
			// short-circuits without hitting Redis again.
			if state == nil {
				state = &breakerState{}
				r.breakers[name] = state
			}
			state.OpenUntil = openUntil
			return true
		}
	}
	return false
}

func orderedPriorities(targets []aliasTarget) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(targets))
	for _, target := range targets {
		if seen[target.Priority] {
			continue
		}
		seen[target.Priority] = true
		out = append(out, target.Priority)
	}
	sort.Ints(out)
	return out
}
