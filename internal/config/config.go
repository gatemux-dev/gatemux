package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server      ServerConfig       `yaml:"server"`
	Payloads    PayloadsConfig     `yaml:"payloads,omitempty"`
	OIDC        OIDCConfig         `yaml:"oidc,omitempty"`
	Telemetry   TelemetryConfig    `yaml:"telemetry,omitempty"`
	Database    DatabaseConfig     `yaml:"database"`
	Redis       RedisConfig        `yaml:"redis"`
	Admin       AdminConfig        `yaml:"admin"`
	Deployments []DeploymentConfig `yaml:"deployments"`
	Aliases     []AliasConfig      `yaml:"aliases"`
	Callbacks   []CallbackConfig   `yaml:"callbacks,omitempty"`
}

// TelemetryConfig controls metrics + tracing export. Metrics are always
// scraped from /metrics on the private listener (server.metrics_addr).
// Tracing is opt-in: leave OTLPEndpoint empty for no trace export
// (spans are still created in-process so the request-id correlates with
// the audit log, but nothing leaves the binary).
type TelemetryConfig struct {
	// ServiceName is reported as service.name on every span. Defaults to
	// "gatemux". Override when you run multiple replicas in the same trace
	// backend and want to see them as separate services.
	ServiceName string `yaml:"service_name,omitempty"`
	// OTLPEndpoint is the OTLP collector address. Empty disables trace
	// export. Examples:
	//   http://otel-collector:4318 (HTTP/protobuf)
	//   otel-collector:4317        (gRPC)
	OTLPEndpoint string `yaml:"otlp_endpoint,omitempty"`
	// OTLPProtocol selects http (default, port 4318) or grpc (port 4317).
	OTLPProtocol string `yaml:"otlp_protocol,omitempty"`
	// SampleRate (0.0–1.0) controls head sampling. 0 disables export
	// (treated as opt-out), 1.0 exports every trace. Defaults to 0.1
	// when OTLPEndpoint is set so production traffic doesn't drown the
	// collector — operators tune up for load testing or down for cost.
	SampleRate float64 `yaml:"sample_rate,omitempty"`
	// StdoutTracing dumps spans to stderr. Useful for local debugging,
	// off in production. Falls back to GATEMUX_OTEL_STDOUT env var if
	// the YAML key is absent so existing dev workflows keep working.
	StdoutTracing bool `yaml:"stdout_tracing,omitempty"`
}

// CallbackConfig configures a single sink. The `type` field selects the
// implementation (webhook, slack, s3, langfuse); other fields are
// type-specific. Field names match the design doc 0003 schema.
type CallbackConfig struct {
	Name       string            `yaml:"name"`
	Type       string            `yaml:"type"`
	EventTypes []string          `yaml:"event_types,omitempty"`
	URL        string            `yaml:"url,omitempty"`
	URLEnv     string            `yaml:"url_env,omitempty"`
	LocalDir   string            `yaml:"local_dir,omitempty"`
	Headers    map[string]string `yaml:"headers,omitempty"`
	// Langfuse-specific
	Host         string `yaml:"host,omitempty"`
	PublicKeyEnv string `yaml:"public_key_env,omitempty"`
	SecretKeyEnv string `yaml:"secret_key_env,omitempty"`
}

type ServerConfig struct {
	Addr        string         `yaml:"addr"`
	ReadTimeout time.Duration  `yaml:"read_timeout"`
	Shutdown    ShutdownConfig `yaml:"shutdown,omitempty"`
	// TrustedProxies is a list of CIDRs whose X-Forwarded-For /
	// X-Real-IP headers are honored when extracting client IP. When
	// empty, the gateway ignores those headers and uses RemoteAddr —
	// the safe default for a directly-exposed deployment. Operators
	// behind an LB or CDN should set this to the LB's CIDR(s).
	TrustedProxies []string `yaml:"trusted_proxies,omitempty"`
	// V1Deadline caps the wall-clock time a single /v1 request can
	// consume across the entire fallback chain. Per-attempt timeouts
	// (45s by default for non-streaming) still apply, but the chain as
	// a whole can never exceed this. Defaults to 90s when unset.
	V1Deadline time.Duration `yaml:"v1_deadline,omitempty"`
	// Streaming controls routed /v1/chat/completions streams. These operator
	// limits cannot be overridden by request JSON; V1Deadline remains the total cap.
	Streaming StreamingConfig `yaml:"streaming,omitempty"`
	// MetricsAddr is the listen address for /metrics. Bound to a private
	// interface (127.0.0.1:9090 by default) so Prometheus labels — which
	// include team slugs — aren't enumerable from the public listener.
	// Set to "" to disable the metrics listener entirely. Set to ":9090"
	// to expose on all interfaces (only do this if your network already
	// gates access).
	MetricsAddr string `yaml:"metrics_addr,omitempty"`
	// Admission bounds the number of long-lived inference requests and the
	// callers allowed to wait for capacity. It is enabled by default because an
	// unbounded semaphore queue still permits memory growth during an upstream
	// slowdown. Set disabled=true only when an external proxy enforces an
	// equivalent bound.
	Admission AdmissionConfig `yaml:"admission,omitempty"`
}

type AdmissionConfig struct {
	Disabled     bool          `yaml:"disabled,omitempty"`
	MaxInFlight  int           `yaml:"max_in_flight,omitempty"`
	MaxQueued    int           `yaml:"max_queued,omitempty"`
	QueueTimeout time.Duration `yaml:"queue_timeout,omitempty"`
}

type ShutdownConfig struct {
	GracePeriod    time.Duration `yaml:"grace_period,omitempty"`
	CleanupTimeout time.Duration `yaml:"cleanup_timeout,omitempty"`
}

func (s ShutdownConfig) WithDefaults() ShutdownConfig {
	if s.GracePeriod == 0 {
		s.GracePeriod = 30 * time.Second
	}
	if s.CleanupTimeout == 0 {
		s.CleanupTimeout = 10 * time.Second
	}
	return s
}

func (s ShutdownConfig) Validate() error {
	if s.GracePeriod < 0 || s.GracePeriod > 24*time.Hour {
		return fmt.Errorf("server.shutdown.grace_period must be between zero and 24h")
	}
	if s.CleanupTimeout < 0 || s.CleanupTimeout > 5*time.Minute {
		return fmt.Errorf("server.shutdown.cleanup_timeout must be between zero and 5m")
	}
	return nil
}

type StreamingConfig struct {
	// FirstEventTimeout includes upstream connection, headers and the first
	// complete non-empty SSE data event (including role/tool/reasoning chunks).
	FirstEventTimeout time.Duration `yaml:"first_event_timeout,omitempty"`
	// IdleTimeout is the maximum gap between complete non-empty data events.
	// Partial bytes and comment-only keepalives do not extend either timeout.
	// Downstream writes pause idle accounting under their own WriteTimeout.
	IdleTimeout time.Duration `yaml:"idle_timeout,omitempty"`
	// WriteTimeout bounds each downstream event write and flush.
	WriteTimeout time.Duration `yaml:"write_timeout,omitempty"`
	// Optional comment frames after the first upstream event. Zero disables.
	// They never extend upstream progress or total request deadlines.
	KeepaliveInterval time.Duration `yaml:"keepalive_interval,omitempty"`
}

// WithDefaults also protects handlers constructed directly by embedders/tests.
func (s StreamingConfig) WithDefaults() StreamingConfig {
	if s.FirstEventTimeout <= 0 {
		s.FirstEventTimeout = 30 * time.Second
	}
	if s.IdleTimeout <= 0 {
		s.IdleTimeout = 30 * time.Second
	}
	if s.WriteTimeout <= 0 {
		s.WriteTimeout = 15 * time.Second
	}
	return s
}

type DatabaseConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

type RedisConfig struct {
	Addr        string `yaml:"addr"`
	PasswordEnv string `yaml:"password_env,omitempty"`
	DB          int    `yaml:"db,omitempty"`
	KeyPrefix   string `yaml:"key_prefix,omitempty"`
}

type AdminConfig struct {
	MasterKeyEnv string `yaml:"master_key_env"`
	// SecureCookies must be enabled behind an HTTPS-terminating proxy.
	// Forwarded headers never decide cookie security.
	SecureCookies bool `yaml:"secure_cookies,omitempty"`
	// DisableMasterKey rejects master-key authentication on every admin route.
	// Provision an administrator account or OIDC access before enabling it.
	DisableMasterKey bool `yaml:"disable_master_key,omitempty"`
	// DisableMasterKeyLogin hides the master-key form on the web Login
	// page only. Bearer authentication remains enabled unless DisableMasterKey
	// is set; hiding a form is not a server-side security control.
	DisableMasterKeyLogin bool `yaml:"disable_master_key_login,omitempty"`
}

// OIDCConfig drives the optional Sign-in-with-IdP flow. When Issuer is
// empty the whole OIDC surface is disabled — /auth/oidc/* returns 404,
// /auth/login-modes reports oidc_enabled=false, and the login page hides
// the SSO button. Single-provider on purpose; multi-IdP support can be
// stacked on top once anyone needs it.
type OIDCConfig struct {
	Issuer          string   `yaml:"issuer,omitempty"`
	ClientID        string   `yaml:"client_id,omitempty"`
	ClientSecretEnv string   `yaml:"client_secret_env,omitempty"`
	RedirectURL     string   `yaml:"redirect_url,omitempty"`
	ProviderName    string   `yaml:"provider_name,omitempty"`
	Scopes          []string `yaml:"scopes,omitempty"`
	// RoleClaim names a claim (e.g. "groups") whose values are matched
	// against RoleMap to assign role on sign-in. Empty disables role
	// mapping; users land at DefaultRole until an admin adjusts.
	RoleClaim   string            `yaml:"role_claim,omitempty"`
	RoleMap     map[string]string `yaml:"role_map,omitempty"`
	DefaultRole string            `yaml:"default_role,omitempty"`
	// TeamFromEmailDomain maps "<domain>" → "<team-slug>" so an
	// engineer at platform.example.com lands on the platform team
	// without a manual assignment. Falls back to DefaultTeam (slug)
	// when no domain matches.
	TeamFromEmailDomain map[string]string `yaml:"team_from_email_domain,omitempty"`
	DefaultTeam         string            `yaml:"default_team,omitempty"`
}

// Enabled returns true when the operator has filled in the bare-minimum
// fields needed to actually negotiate with the IdP. We don't surface
// errors for half-configured blocks — the OIDC surface stays 404 and the
// boot log warns once.
func (o OIDCConfig) Enabled() bool {
	return o.Issuer != "" && o.ClientID != "" && o.RedirectURL != ""
}

// PayloadsConfig controls retention for opt-in /v1 request body capture.
// Capture itself is per-team (teams.capture_payloads) and off by default.
// Retention is global: if a team enables capture, payloads older than
// RetentionHours are pruned by a background goroutine. The pruner runs
// every PruneIntervalHours.
type PayloadsConfig struct {
	RetentionHours     int `yaml:"retention_hours,omitempty"`
	PruneIntervalHours int `yaml:"prune_interval_hours,omitempty"`
}

type DeploymentConfig struct {
	Name                string           `yaml:"name"`
	Type                string           `yaml:"type"`
	UpstreamModel       string           `yaml:"upstream_model"`
	APIKeyEnv           string           `yaml:"api_key_env"`
	BaseURL             string           `yaml:"base_url,omitempty"`
	Region              string           `yaml:"region,omitempty"`
	MaxParallelRequests int              `yaml:"max_parallel_requests,omitempty"`
	Streaming           *StreamingConfig `yaml:"streaming,omitempty"`
}

type AliasConfig struct {
	Alias       string   `yaml:"alias"`
	Deployments []string `yaml:"deployments"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	c.Server.Shutdown = c.Server.Shutdown.WithDefaults()
	// Keep negatives intact so validate rejects invalid operator settings.
	defaults := c.Server.Streaming.WithDefaults()
	if c.Server.Streaming.FirstEventTimeout == 0 {
		c.Server.Streaming.FirstEventTimeout = defaults.FirstEventTimeout
	}
	if c.Server.Streaming.IdleTimeout == 0 {
		c.Server.Streaming.IdleTimeout = defaults.IdleTimeout
	}
	if c.Server.Streaming.WriteTimeout == 0 {
		c.Server.Streaming.WriteTimeout = defaults.WriteTimeout
	}
	if c.Server.Addr == "" {
		c.Server.Addr = ":4000"
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 30 * time.Second
	}
	if c.Server.V1Deadline == 0 {
		c.Server.V1Deadline = 90 * time.Second
	}
	// Default the metrics listener to a localhost-only port. Operators
	// who want to expose it (e.g. a sidecar Prometheus inside the same
	// pod scraping over the loopback) can override; the empty string
	// disables the listener entirely.
	if c.Server.MetricsAddr == "" {
		c.Server.MetricsAddr = "127.0.0.1:9090"
	}
	if c.Server.Admission.MaxInFlight == 0 {
		c.Server.Admission.MaxInFlight = 1024
	}
	if c.Server.Admission.MaxQueued == 0 {
		c.Server.Admission.MaxQueued = 256
	}
	if c.Server.Admission.QueueTimeout == 0 {
		c.Server.Admission.QueueTimeout = 250 * time.Millisecond
	}
	if c.Telemetry.ServiceName == "" {
		c.Telemetry.ServiceName = "gatemux"
	}
	if c.Telemetry.OTLPProtocol == "" {
		c.Telemetry.OTLPProtocol = "http"
	}
	// SampleRate==0 with no endpoint means "no tracing" which is fine.
	// SampleRate==0 with an endpoint set is almost certainly a config
	// mistake (operator wired up an OTLP endpoint expecting traces);
	// pick a sane default rather than silently dropping every span.
	if c.Telemetry.OTLPEndpoint != "" && c.Telemetry.SampleRate == 0 {
		c.Telemetry.SampleRate = 0.1
	}
	if c.Redis.KeyPrefix == "" {
		// Storage identity, not branding: preserve omitted-prefix installations.
		// New sample configurations explicitly select "gatemux".
		c.Redis.KeyPrefix = "aiport"
	}
}

func (c *Config) validate() error {
	if err := c.Server.Shutdown.Validate(); err != nil {
		return err
	}
	if len(c.Callbacks) > 32 {
		return fmt.Errorf("at most 32 callbacks may be configured")
	}
	if err := c.Server.Streaming.Validate(); err != nil {
		return fmt.Errorf("server.%w", err)
	}
	if c.Server.Admission.MaxInFlight < 0 {
		return fmt.Errorf("server.admission.max_in_flight must be non-negative")
	}
	if c.Server.Admission.MaxQueued < 0 {
		return fmt.Errorf("server.admission.max_queued must be non-negative")
	}
	if c.Server.Admission.QueueTimeout < 0 {
		return fmt.Errorf("server.admission.queue_timeout must be non-negative")
	}
	if c.Database.Driver != "postgres" {
		return fmt.Errorf(`database.driver must be "postgres"`)
	}
	if c.Database.DSN == "" {
		return fmt.Errorf("database.dsn is required")
	}
	if c.Admin.MasterKeyEnv == "" {
		return fmt.Errorf("admin.master_key_env is required")
	}
	if Env(c.Admin.MasterKeyEnv) == "" {
		return fmt.Errorf("env var %q (admin.master_key_env) is empty", c.Admin.MasterKeyEnv)
	}
	if c.Redis.PasswordEnv != "" && os.Getenv(c.Redis.PasswordEnv) == "" {
		return fmt.Errorf("env var %q (redis.password_env) is empty", c.Redis.PasswordEnv)
	}

	deployNames := map[string]bool{}
	for _, d := range c.Deployments {
		if d.Name == "" {
			return fmt.Errorf("deployment with empty name")
		}
		if deployNames[d.Name] {
			return fmt.Errorf("duplicate deployment name: %s", d.Name)
		}
		deployNames[d.Name] = true
		if d.Type == "" {
			return fmt.Errorf("deployment %q: type is required", d.Name)
		}
		if d.UpstreamModel == "" {
			return fmt.Errorf("deployment %q: upstream_model is required", d.Name)
		}
		if d.APIKeyEnv == "" {
			return fmt.Errorf("deployment %q: api_key_env is required", d.Name)
		}
		if d.MaxParallelRequests < 0 {
			return fmt.Errorf("deployment %q: max_parallel_requests must be non-negative", d.Name)
		}
		if d.Streaming != nil {
			if err := d.Streaming.Validate(); err != nil {
				return fmt.Errorf("deployment %q: %w", d.Name, err)
			}
		}
	}

	aliases := map[string]bool{}
	for _, a := range c.Aliases {
		if a.Alias == "" {
			return fmt.Errorf("alias with empty name")
		}
		if aliases[a.Alias] {
			return fmt.Errorf("duplicate alias: %s", a.Alias)
		}
		aliases[a.Alias] = true
		if len(a.Deployments) == 0 {
			return fmt.Errorf("alias %q: must reference at least one deployment", a.Alias)
		}
		for _, dep := range a.Deployments {
			if !deployNames[dep] {
				return fmt.Errorf("alias %q: deployment %q is not defined", a.Alias, dep)
			}
		}
	}

	return nil
}
