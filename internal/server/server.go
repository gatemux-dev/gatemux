package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/gatemux-dev/gatemux/internal/admission"
	"github.com/gatemux-dev/gatemux/internal/alerts"
	"github.com/gatemux-dev/gatemux/internal/api"
	"github.com/gatemux-dev/gatemux/internal/apidocs"
	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/budget"
	"github.com/gatemux-dev/gatemux/internal/cache"
	"github.com/gatemux-dev/gatemux/internal/callbacks"
	distconcurrency "github.com/gatemux-dev/gatemux/internal/concurrency"
	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/ratelimit"
	"github.com/gatemux-dev/gatemux/internal/router"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/gatemux-dev/gatemux/internal/telemetry"
	"github.com/gatemux-dev/gatemux/internal/usage"
	"github.com/gatemux-dev/gatemux/web"
)

type Server struct {
	requestCtx     context.Context
	requestCancel  context.CancelFunc
	requestsMu     sync.Mutex
	requestsDone   chan struct{}
	activeRequests int
	draining       atomic.Bool
	workerCancel   context.CancelFunc
	workerDone     []<-chan struct{}
	shutdownOnce   sync.Once
	shutdownErr    error
	serveMu        sync.Mutex
	serveDone      chan struct{}
	metricsDone    chan struct{}
	promptCache    *cache.Cache
	breakerClient  *redis.Client
	cfg            *config.Config
	store          *store.Store
	logger         *slog.Logger
	http           *http.Server
	metrics        *http.Server
	usage          *usage.Logger
	limit          *ratelimit.Limiter
	tel            *telemetry.Collector
	callbacks      *callbacks.Bus
	admission      *admission.Gate
	concurrency    *distconcurrency.Limiter
	policyRegistry *router.Registry
	jwtVerifier    *auth.JWTVerifier
}

func New(cfg *config.Config, st *store.Store, logger *slog.Logger) (*Server, error) {
	if err := cfg.Server.Shutdown.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.Callbacks) > 32 {
		return nil, fmt.Errorf("at most 32 callbacks may be configured")
	}
	masterKey := config.Env(cfg.Admin.MasterKeyEnv)
	if masterKey == "" {
		return nil, fmt.Errorf("admin master key env %q is empty", cfg.Admin.MasterKeyEnv)
	}
	if cfg.Admin.DisableMasterKey {
		masterKey = "" // all Session middleware below rejects this credential
	}

	bootCtx := context.Background()
	for _, d := range cfg.Deployments {
		var baseURL, region *string
		if d.BaseURL != "" {
			baseURL = &d.BaseURL
		}
		if d.Region != "" {
			region = &d.Region
		}
		var maxParallelRequests *int
		if d.MaxParallelRequests > 0 {
			maxParallelRequests = &d.MaxParallelRequests
		}
		if _, err := st.UpsertDeployment(bootCtx, d.Name, d.Type, d.UpstreamModel, d.APIKeyEnv, baseURL, region, nil, maxParallelRequests, d.Streaming); err != nil {
			return nil, fmt.Errorf("sync deployment %q: %w", d.Name, err)
		}
	}
	for _, a := range cfg.Aliases {
		if _, err := st.UpsertAlias(bootCtx, a.Alias, a.Deployments); err != nil {
			return nil, fmt.Errorf("sync alias %q: %w", a.Alias, err)
		}
	}

	reg, err := router.New(st, logger)
	if err != nil {
		return nil, fmt.Errorf("build router: %w", err)
	}

	tel, err := telemetry.NewWithOptions(telemetry.Options{
		Registry:       prometheus.NewRegistry(),
		ServiceName:    cfg.Telemetry.ServiceName,
		ServiceVersion: "0.0.0-dev",
		OTLPEndpoint:   cfg.Telemetry.OTLPEndpoint,
		OTLPProtocol:   cfg.Telemetry.OTLPProtocol,
		SampleRate:     cfg.Telemetry.SampleRate,
		StdoutTracing:  cfg.Telemetry.StdoutTracing || config.Env("GATEMUX_OTEL_STDOUT") != "",
	})
	if err != nil {
		return nil, fmt.Errorf("setup telemetry: %w", err)
	}
	usageLogger := usage.NewLogger(st, logger, tel)
	rl := ratelimit.New()
	var promptCache *cache.Cache
	var breakerStore router.BreakerStore
	var tenantConcurrency *distconcurrency.Limiter
	var breakerClient *redis.Client
	if cfg.Redis.Addr != "" {
		redisPassword := ""
		if cfg.Redis.PasswordEnv != "" {
			redisPassword = os.Getenv(cfg.Redis.PasswordEnv)
		}
		rl = ratelimit.NewRedis(cfg.Redis.Addr, redisPassword, cfg.Redis.DB, cfg.Redis.KeyPrefix)
		tenantConcurrency = distconcurrency.NewRedis(cfg.Redis.Addr, redisPassword, cfg.Redis.DB, cfg.Redis.KeyPrefix)
		promptCache = cache.New(cfg.Redis.Addr, redisPassword, cfg.Redis.DB, cfg.Redis.KeyPrefix)
		// Distinct redis client for the breaker store so its tight IO
		// timeouts don't share a connection pool with cache writes.
		breakerClient = redis.NewClient(&redis.Options{
			Addr:        cfg.Redis.Addr,
			Password:    redisPassword,
			DB:          cfg.Redis.DB,
			DialTimeout: 50 * time.Millisecond, ReadTimeout: 50 * time.Millisecond,
			WriteTimeout: 50 * time.Millisecond, PoolTimeout: 50 * time.Millisecond,
			ContextTimeoutEnabled: true, MaxRetries: -1,
		})
		breakerStore = router.NewRedisBreakerStore(breakerClient, cfg.Redis.KeyPrefix)
	}
	if breakerStore != nil {
		reg.SetBreakerStore(breakerStore)
	}
	bus := buildCallbackBus(cfg, logger)
	var admissionGate *admission.Gate
	if !cfg.Server.Admission.Disabled {
		admissionGate = admission.New(admission.Config{
			MaxInFlight:  cfg.Server.Admission.MaxInFlight,
			MaxQueued:    cfg.Server.Admission.MaxQueued,
			QueueTimeout: cfg.Server.Admission.QueueTimeout,
		})
	}
	jwtVerifier := auth.NewJWTVerifier(st)
	if err := jwtVerifier.Refresh(bootCtx); err != nil {
		logger.Warn("jwt verifier initial refresh failed", "err", err)
	}
	s := &Server{cfg: cfg, store: st, logger: logger, usage: usageLogger, limit: rl, tel: tel, callbacks: bus, admission: admissionGate, concurrency: tenantConcurrency, policyRegistry: reg, promptCache: promptCache, breakerClient: breakerClient}
	s.initLifecycle()
	s.jwtVerifier = jwtVerifier
	built := false
	defer func() {
		if !built {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.Shutdown(cleanup)
		}
	}()

	inflight := &atomic.Int64{}
	adminH := &api.AdminHandler{
		Store:     st,
		Config:    cfg,
		Version:   "0.0.0-dev",
		StartedAt: time.Now(),
		Inflight:  inflight,
		Registry:  reg,
		Logger:    logger,
		Callbacks: bus,
	}
	v1H := &api.V1Handler{Logger: logger, Router: reg, Usage: usageLogger, RateLimit: rl, Budget: budget.New(st), Telemetry: tel, Cache: promptCache, Callbacks: bus, Concurrency: tenantConcurrency, Streaming: cfg.Server.Streaming}
	adminH.V1 = v1H
	authH := &api.AuthHandler{Store: st, Logger: logger, SecureCookies: cfg.Admin.SecureCookies, MasterKeyLoginDisabled: cfg.Admin.DisableMasterKeyLogin || cfg.Admin.DisableMasterKey}
	resetH := &api.PasswordResetHandler{Store: st}

	// OIDC handler. Discovery happens at boot so issuer/JWKS are
	// validated up-front; a misconfigured block fails the boot rather
	// than silently 5xx-ing first sign-ins. When OIDC isn't configured
	// at all, NewOIDCHandler returns a no-op handler that 404s.
	oidcSecret := os.Getenv(cfg.OIDC.ClientSecretEnv)
	oidcH, err := api.NewOIDCHandler(context.Background(), st, logger, cfg.OIDC, oidcSecret)
	if err != nil {
		logger.Warn("oidc disabled", "err", err)
		oidcH = &api.OIDCHandler{Store: st, Logger: logger, Cfg: cfg.OIDC}
	}
	oidcH.SecureCookies = cfg.Admin.SecureCookies
	authH.OIDCEnabled = oidcH.Enabled()
	authH.OIDCProviderName = cfg.OIDC.ProviderName
	inviteH := &api.InviteHandler{Store: st, SecureCookies: cfg.Admin.SecureCookies}
	meH := &api.MeHandler{Store: st, Budget: budget.New(st), Logger: logger}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	r.Use(auth.WithTrustedProxies(cfg.Server.TrustedProxies))
	r.Use(tel.Middleware)
	r.Use(accessLog(logger))

	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.With(limitBody(bodyLimitAdmin), auth.Session(masterKey, st), auth.RequireAdmin).Post("/health/drain", s.handleDrain)
	// /metrics deliberately NOT mounted here — it's served on a separate
	// listener (cfg.Server.MetricsAddr, default 127.0.0.1:9090) because
	// Prometheus labels include high-cardinality team slugs that are not
	// public-safe. See start() for the listener wiring.

	throttle := newLoginThrottle(st)
	throttle.redis, throttle.prefix = breakerClient, cfg.Redis.KeyPrefix
	r.Route("/auth", func(r chi.Router) {
		r.Use(limitBody(bodyLimitAdmin))
		// Login is the only entry that takes anonymous credentials, so the
		// throttle wraps that one route — Whoami and Logout are session-
		// authed and the session middleware blocks them on its own.
		r.With(throttle.middleware).Post("/login", authH.Login)
		r.Get("/login-modes", authH.LoginModesHandler)
		// OIDC start/callback are unauthenticated — they ARE the auth
		// flow. They 404 when oidcH.Enabled() is false.
		r.Get("/oidc/start", oidcH.Start)
		r.Get("/oidc/callback", oidcH.Callback)
		r.Group(func(r chi.Router) {
			r.Use(auth.Session(masterKey, st))
			r.Get("/me", authH.Whoami)
			r.Post("/logout", authH.Logout)
		})
	})

	r.Route("/invite", func(r chi.Router) {
		r.Use(limitBody(bodyLimitAdmin))
		r.Get("/{token}", inviteH.GetInvite)
		r.Post("/{token}/accept", inviteH.AcceptInvite)
	})

	// Public password-reset surface. The token itself is the credential,
	// so no session middleware — but we still gate body size and apply
	// the login throttle to the consume endpoint to slow brute-force
	// guessing of valid tokens.
	r.Route("/reset", func(r chi.Router) {
		r.Use(limitBody(bodyLimitAdmin))
		r.Get("/{token}", resetH.Get)
		r.With(throttle.middleware).Post("/{token}", resetH.Consume)
	})

	r.Route("/me", func(r chi.Router) {
		r.Use(limitBody(bodyLimitAdmin))
		r.Use(auth.Session(masterKey, st))
		r.Get("/keys", meH.ListKeys)
		r.Get("/usage", meH.ListUsage)
		r.Get("/budget", meH.GetBudget)
		r.Get("/sessions", meH.ListSessions)
		r.Delete("/sessions/{prefix}", meH.RevokeSession)
		r.Delete("/sessions", meH.RevokeOtherSessions)
		r.Post("/password", meH.ChangePassword)
	})

	r.Route("/admin", func(r chi.Router) {
		r.Use(limitBody(bodyLimitAdmin))
		r.Use(auth.Session(masterKey, st))
		r.Use(adminH.InflightMiddleware)

		// Admin-only — global control plane (models, pricing, users CRUD,
		// cross-tenant audit, team creation).
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Get("/info", adminH.GetInfo)
			r.Get("/audit", adminH.ListAudit)
			r.Get("/audit/facets", adminH.GetAuditFacets)
			r.Get("/users", adminH.ListUsers)
			r.Post("/users/{id}/budget", adminH.UpdateUserBudget)
			r.Patch("/users/{id}/concurrency", adminH.UpdateUserConcurrency)
			r.Post("/users/{id}/password-reset", adminH.IssuePasswordReset)
			r.Patch("/users/{id}/disabled", adminH.SetUserDisabled)
			r.Patch("/users/{id}/role", adminH.SetUserRole)
			r.Post("/teams", adminH.CreateTeam)
			r.Get("/pricing", adminH.ListPricing)
			r.Post("/pricing", adminH.UpsertPricing)
			r.Get("/deployments", adminH.ListDeployments)
			r.Get("/concurrency/routing", adminH.ListRoutingConcurrencyLimits)
			r.Put("/concurrency/routing", adminH.SetRoutingConcurrencyLimit)
			r.Get("/provider-health", adminH.GetProviderHealth)
			r.Get("/provider-health/{name}/history", adminH.GetProviderHealthHistory)
			r.Post("/deployments", adminH.CreateDeployment)
			r.Patch("/deployments/{name}", adminH.UpdateDeployment)
			r.Post("/deployments/{name}/test", adminH.TestDeploymentConnection)
			r.Delete("/deployments/{name}", adminH.DeleteDeployment)
			r.Get("/aliases", adminH.ListAliases)
			r.Post("/aliases", adminH.UpsertAlias)
			r.Patch("/aliases/{alias}/cache", adminH.UpdateAliasCache)
			r.Patch("/aliases/{alias}/strategy", adminH.UpdateAliasStrategy)
			r.Patch("/deployments/{name}/tags", adminH.UpdateDeploymentTagsHandler)
			r.Delete("/aliases/{alias}", adminH.DeleteAlias)
			r.Get("/callbacks", adminH.ListCallbackStats)
			r.Get("/projections", adminH.GetProjection)
			r.Get("/export/usage.csv", adminH.ExportUsage)
			r.Get("/export/audit.csv", adminH.ExportAudit)
			r.Get("/alerts", adminH.ListAlertRules)
			r.Post("/alerts", adminH.CreateAlertRule)
			r.Patch("/alerts/{id}", adminH.UpdateAlertRule)
			r.Delete("/alerts/{id}", adminH.DeleteAlertRule)
			r.Get("/alert-events", adminH.ListAlertEvents)
			r.Get("/guardrails", adminH.ListGuardrails)
			r.Get("/guardrails/assignments", adminH.ListGuardrailAssignments)
			r.Post("/guardrails/test", adminH.TestGuardrails)
			r.Get("/guardrails/{scope}/{subject}", adminH.GetGuardrailScope)
			r.Put("/guardrails/{scope}/{subject}", adminH.SetGuardrailScope)
			r.Get("/usage/{id}/payload", adminH.GetUsagePayload)
			r.With(s.inferenceLifecycle, deadlineMiddleware(cfg.Server.V1Deadline), admissionMiddleware(admissionGate, tel), s.rejectDrained).Post("/usage/{id}/replay", adminH.ReplayUsage)
			r.Get("/keys/{id}/effective-policy", adminH.GetEffectivePolicy)
			r.Patch("/teams/{slug}/capture-payloads", adminH.UpdateTeamCapturePayloads)
			r.Put("/teams/{slug}/models", adminH.UpdateTeamAllowedModels)
			r.Get("/passthroughs", adminH.ListPassthroughs)
			r.Post("/passthroughs", adminH.CreatePassthrough)
			r.Patch("/passthroughs/{name}", adminH.UpdatePassthrough)
			r.Delete("/passthroughs/{name}", adminH.DeletePassthrough)
		})

		// Team-scoped — admin OR manager of the team in {slug}. The
		// middleware enforces the team match before the handler runs.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireTeamAccess("slug"))
			r.Get("/teams/{slug}", adminH.GetTeam)
			r.Get("/teams/{slug}/members", adminH.ListTeamMembers)
			r.Get("/teams/{slug}/models", adminH.ListTeamModels)
			r.Post("/teams/{slug}/budget", adminH.UpdateTeamBudget)
			r.Patch("/teams/{slug}/concurrency", adminH.UpdateTeamConcurrency)
			r.Patch("/teams/{slug}/rates", adminH.UpdateTeamRates)
			r.Get("/teams/{slug}/keys", adminH.ListKeys)
			r.Post("/teams/{slug}/keys", adminH.CreateKey)
			r.Get("/teams/{slug}/customers", adminH.ListCustomers)
			r.Patch("/teams/{slug}/customer-policy", adminH.SetCustomerRegistration)
			r.Post("/teams/{slug}/customers", adminH.CreateCustomer)
			r.Patch("/teams/{slug}/customers/{externalID}", adminH.UpdateCustomer)
			r.Get("/teams/{slug}/customers/{externalID}/budget", adminH.GetCustomerBudget)
			r.Patch("/teams/{slug}/customers/{externalID}/concurrency", adminH.SetCustomerConcurrency)
			r.Get("/teams/{slug}/service-accounts", adminH.ListServiceAccounts)
			r.Post("/teams/{slug}/service-accounts", adminH.CreateServiceAccount)
		})

		// Service-account scoped routes — SA id is enough; ownership
		// (team membership) is checked inside the handler via
		// GetServiceAccountByID, which returns 404 for foreign SAs.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Patch("/service-accounts/{id}", adminH.UpdateServiceAccount)
			r.Patch("/service-accounts/{id}/concurrency", adminH.UpdateServiceAccountConcurrency)
			r.Delete("/service-accounts/{id}", adminH.ArchiveServiceAccount)
			r.Post("/service-accounts/{id}/keys", adminH.CreateServiceAccountKey)
		})

		// Manager-or-admin — handler does its own per-team filtering.
		// Members can't reach these; managers see only their own team's
		// rows; admins see everything.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireManagerOrAdmin)
			r.Get("/teams", adminH.ListTeams)
			r.Get("/usage", adminH.ListUsage)
			r.Get("/usage/facets", adminH.GetUsageFacets)
			r.Get("/usage/{id}", adminH.GetUsageRow)
			r.Get("/usage/aggregate", adminH.GetUsageAggregate)
			r.Get("/spend", adminH.GetSpendReport)
			r.Get("/spend/timeseries", adminH.GetSpendTimeseries)
			r.Get("/invites", adminH.ListInvites)
			r.Post("/invites", adminH.CreateInvite)
			r.Patch("/keys/{id}", adminH.UpdateKey)
			r.Get("/keys/{id}/budget", adminH.GetKeyBudget)
			r.Patch("/keys/{id}/budget", adminH.SetKeyBudget)
			r.Post("/keys/{id}/rotate", adminH.RotateKey)
			r.Post("/keys/{id}/revoke", adminH.RevokeKey)
			r.Post("/keys/{id}/pause", adminH.PauseKey)
			r.Post("/keys/{id}/resume", adminH.ResumeKey)
		})
	})

	r.Route("/v1", func(r chi.Router) {
		r.Use(s.inferenceLifecycle)
		r.Use(limitBody(bodyLimitV1))
		// Cap total wall time for the entire fallback chain. Per-attempt
		// timeouts still apply on each upstream call; this is the upper
		// bound the operator agreed to wait, regardless of how many
		// retries the router does.
		r.Use(deadlineMiddleware(cfg.Server.V1Deadline))
		r.Use(admissionMiddleware(admissionGate, tel))
		r.Use(s.rejectDrained)
		r.Use(auth.Bearer(st, jwtVerifier))
		r.Use(tenantConcurrencyMiddleware(tenantConcurrency, concurrencyLeaseTTL(cfg.Server.V1Deadline), tel, logger))
		r.Get("/models", v1H.Models)
		r.Post("/chat/completions", v1H.ChatCompletions)
		r.Post("/embeddings", v1H.Embeddings)
		r.Post("/responses", v1H.Responses)
		r.Get("/responses/{responseID}", v1H.GetResponse)
		r.Delete("/responses/{responseID}", v1H.DeleteResponse)
		r.Get("/responses/{responseID}/input_items", v1H.ResponseInputItems)
		r.Post("/moderations", v1H.Moderations)
		r.Post("/rerank", v1H.Rerank)
		r.Post("/images/generations", v1H.ImagesGenerations)
		r.Post("/messages", v1H.Messages)
		r.Post("/audio/transcriptions", v1H.AudioTranscriptions)
		r.Post("/audio/translations", v1H.AudioTranslations)
		r.Post("/audio/speech", v1H.AudioSpeech)
	})

	// /passthrough/{name}/* — generic proxy to operator-configured upstream
	// targets. Same Bearer auth, deadline, and body-size envelope as /v1.
	// Verb-agnostic so operators can use whatever the upstream expects
	// (POST for OpenAI Files create, GET for list, DELETE for cancel, etc.).
	r.Route("/passthrough", func(r chi.Router) {
		r.Use(s.inferenceLifecycle)
		r.Use(limitBody(bodyLimitV1))
		r.Use(deadlineMiddleware(cfg.Server.V1Deadline))
		r.Use(admissionMiddleware(admissionGate, tel))
		r.Use(s.rejectDrained)
		r.Use(auth.Bearer(st, jwtVerifier))
		r.Use(tenantConcurrencyMiddleware(tenantConcurrency, concurrencyLeaseTTL(cfg.Server.V1Deadline), tel, logger))
		r.HandleFunc("/{name}/*", v1H.GenericPassthrough)
	})

	if err := apidocs.Mount(r); err != nil {
		return nil, fmt.Errorf("build API documentation: %w", err)
	}
	if webFS, err := web.Assets(); err != nil {
		logger.Warn("web ui assets not embedded; UI disabled", "err", err)
	} else {
		r.Get("/*", spaHandler(webFS))
	}

	s.http = &http.Server{
		Addr:        cfg.Server.Addr,
		Handler:     r,
		ReadTimeout: cfg.Server.ReadTimeout,
	}

	// Metrics listener — separate http.Server bound to a private interface
	// (cfg.Server.MetricsAddr, default 127.0.0.1:9090). The audit flagged
	// /metrics on the public listener because team slugs are emitted as
	// labels and shouldn't be enumerable from the internet.
	if cfg.Server.MetricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", tel.MetricsHandler())
		s.metrics = &http.Server{
			Addr:        cfg.Server.MetricsAddr,
			Handler:     mux,
			ReadTimeout: cfg.Server.ReadTimeout,
		}
	}
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	s.workerCancel = cancelWorkers
	s.workerDone = []<-chan struct{}{
		jwtVerifier.StartBackgroundRefresh(workerCtx),
		alerts.New(st, logger).Start(workerCtx),
		startPayloadRetention(workerCtx, st, cfg.Payloads, logger),
		startProviderHealthSampler(workerCtx, st, reg, tel, logger),
		startResponseRetention(workerCtx, st, logger),
	}
	policyDone := make(chan struct{})
	go func() { defer close(policyDone); reg.WatchConcurrencyPolicies(workerCtx) }()
	s.workerDone = append(s.workerDone, policyDone)
	if bus != nil {
		s.workerDone = append(s.workerDone, bus.Start(workerCtx))
	}
	built = true
	return s, nil
}

func buildCallbackBus(cfg *config.Config, log *slog.Logger) *callbacks.Bus {
	if len(cfg.Callbacks) == 0 {
		return nil
	}
	bus := callbacks.NewBus(log)
	for _, c := range cfg.Callbacks {
		url := c.URL
		if c.URLEnv != "" {
			if v := os.Getenv(c.URLEnv); v != "" {
				url = v
			}
		}
		var subs []callbacks.EventType
		for _, t := range c.EventTypes {
			subs = append(subs, callbacks.EventType(t))
		}
		switch c.Type {
		case "webhook":
			bus.Register(&callbacks.Webhook{NameValue: c.Name, URL: url, Headers: c.Headers, Subscribed: subs})
		case "slack":
			bus.Register(&callbacks.Slack{NameValue: c.Name, URL: url, Subscribed: subs})
		case "s3":
			bus.Register(&callbacks.S3Archive{NameValue: c.Name, LocalDir: c.LocalDir, Subscribed: subs})
		case "langfuse":
			pubKey := os.Getenv(c.PublicKeyEnv)
			secKey := os.Getenv(c.SecretKeyEnv)
			if pubKey == "" || secKey == "" {
				log.Warn("langfuse callback missing credentials, skipping", "name", c.Name)
				continue
			}
			bus.Register(&callbacks.Langfuse{
				NameValue:  c.Name,
				Host:       c.Host,
				PublicKey:  pubKey,
				SecretKey:  secKey,
				Subscribed: subs,
			})
		default:
			log.Warn("unknown callback type, skipping", "name", c.Name, "type", c.Type)
		}
	}
	return bus
}

func (s *Server) Start() error {
	// Serialize listener ownership with shutdown. A Server has one lifetime;
	// retrying a failed startup requires a new instance.
	s.serveMu.Lock()
	if s.draining.Load() || s.serveDone != nil {
		s.serveMu.Unlock()
		return fmt.Errorf("server already started or draining")
	}
	s.serveDone = make(chan struct{})
	defer close(s.serveDone)
	listener, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		s.serveMu.Unlock()
		return fmt.Errorf("listen: %w", err)
	}
	metricsErrors := make(chan error, 1)
	if s.metrics != nil {
		metricsListener, err := net.Listen("tcp", s.metrics.Addr)
		if err != nil {
			_ = listener.Close()
			s.serveMu.Unlock()
			return fmt.Errorf("metrics listen: %w", err)
		}
		s.metricsDone = make(chan struct{})
		go func() {
			defer close(s.metricsDone)
			if err := s.metrics.Serve(metricsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				metricsErrors <- fmt.Errorf("metrics serve: %w", err)
				_ = s.http.Close()
			}
		}()
	}
	s.serveMu.Unlock()
	s.logger.Info("server starting", "addr", listener.Addr().String())
	if err := s.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	select {
	case err := <-metricsErrors:
		return err
	default:
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() { s.shutdownErr = s.shutdown(ctx) })
	return s.shutdownErr
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()
	if s.usage != nil && s.usage.Ready() != nil {
		http.Error(w, "accounting recovery pending", http.StatusServiceUnavailable)
		return
	}
	if err := s.store.Pool.Ping(ctx); err != nil {
		http.Error(w, "db unreachable", http.StatusServiceUnavailable)
		return
	}
	if s.limit != nil {
		if err := s.limit.Ready(ctx); err != nil {
			http.Error(w, "redis unreachable", http.StatusServiceUnavailable)
			return
		}
	}
	if s.draining.Load() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

// deadlineMiddleware caps the entire request lifetime, including the
// fallback chain inside v1Handler. When the deadline is reached the
// context cancels, propagating to in-flight upstream calls. Per-attempt
// timeouts inside the router still apply — this is just the *outer*
// bound.
func deadlineMiddleware(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if d <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			// Context cancellation alone cannot interrupt a blocked inbound upload.
			// Join the callback before net/http reuses the writer/connection.
			done := make(chan struct{})
			stop := context.AfterFunc(ctx, func() {
				defer close(done)
				_ = http.NewResponseController(w).SetReadDeadline(time.Now())
			})
			defer func() {
				if !stop() {
					<-done
				}
			}()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// admissionMiddleware protects the entire authenticated data plane, including
// database-backed auth and long-lived streams. The gate bounds both active and
// waiting requests so an upstream slowdown cannot create an unbounded goroutine
// backlog. Capacity errors are deliberately OpenAI-shaped for SDK compatibility.
func admissionMiddleware(gate *admission.Gate, tel *telemetry.Collector) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if gate == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			permit, decision, err := gate.Acquire(r.Context())
			stats := gate.Stats()
			if tel != nil {
				tel.RecordAdmission(string(decision.Outcome), decision.Waited, stats.InFlight, stats.Queued)
			}
			if err != nil {
				if r.Context().Err() != nil {
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"message":"gateway is at capacity; retry later","type":"server_overloaded","param":null,"code":"server_overloaded"}}`))
				return
			}
			defer func() {
				permit.Release()
				if tel != nil {
					stats := gate.Stats()
					tel.SetAdmissionState(stats.InFlight, stats.Queued)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

var tenantRequestSequence atomic.Uint64

// tenantConcurrencyMiddleware acquires all configured distributed scope leases
// after bearer authentication and holds them for the complete handler lifetime.
// That includes SSE and streamed response copies because ServeHTTP does not
// return until the stream is finished. GET /v1/models performs no upstream work
// and intentionally consumes no tenant capacity.
func tenantConcurrencyMiddleware(
	limiter *distconcurrency.Limiter,
	leaseTTL time.Duration,
	tel *telemetry.Collector,
	logger *slog.Logger,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.TrimSuffix(r.URL.Path, "/") == "/v1/models" {
				next.ServeHTTP(w, r)
				return
			}
			team := auth.TeamFromContext(r.Context())
			key := auth.VirtualKeyFromContext(r.Context())
			partition, scopes := tenantConcurrencyScopes(team, key)
			if len(scopes) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			if limiter == nil {
				if tel != nil {
					tel.RecordTenantConcurrency("combined", "unavailable")
				}
				w.Header().Set("Retry-After", "1")
				writeConcurrencyError(w, http.StatusServiceUnavailable, "concurrency_limit_unavailable", "distributed concurrency requires Redis")
				return
			}
			requestID := middleware.GetReqID(r.Context())
			leaseMember := newTenantLeaseMember()
			lease, result, err := limiter.Acquire(r.Context(), distconcurrency.Request{
				ID:        leaseMember,
				Partition: partition,
				TTL:       leaseTTL,
				Scopes:    scopes,
			})
			if err != nil {
				if tel != nil {
					tel.RecordTenantConcurrency("combined", "unavailable")
				}
				if logger != nil {
					logger.Error("distributed concurrency unavailable", "request_id", requestID, "err", err)
				}
				w.Header().Set("Retry-After", "1")
				writeConcurrencyError(w, http.StatusServiceUnavailable, "concurrency_limit_unavailable", "distributed concurrency check unavailable")
				return
			}
			if !result.Allowed {
				if tel != nil {
					tel.RecordTenantConcurrency(result.Scope, "denied")
				}
				w.Header().Set("Retry-After", retryAfterSeconds(result.RetryAfter))
				writeConcurrencyError(w, http.StatusTooManyRequests, "concurrency_limit_exceeded", result.Scope+" concurrent request limit exceeded; retry later")
				return
			}
			if tel != nil {
				tel.RecordTenantConcurrency("combined", "admitted")
			}
			defer func() {
				if err := lease.Release(); err != nil {
					if tel != nil {
						tel.RecordTenantConcurrency("combined", "release_error")
					}
					if logger != nil {
						logger.Error("distributed concurrency lease release failed", "request_id", requestID, "err", err)
					}
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func tenantConcurrencyScopes(team *store.Team, key *store.VirtualKey) (int64, []distconcurrency.Scope) {
	scopes := make([]distconcurrency.Scope, 0, 3)
	var partition int64
	if team != nil && team.MaxParallelRequests != nil && *team.MaxParallelRequests > 0 {
		partition = team.ID
		scopes = append(scopes, distconcurrency.Scope{Kind: "team", ID: team.ID, Limit: *team.MaxParallelRequests})
	}
	// JWT-authenticated callers use a synthetic key with ID zero. Their team
	// limit still applies, but there is no persistent key or owner scope.
	if key != nil && key.ID > 0 && key.MaxParallelRequests != nil && *key.MaxParallelRequests > 0 {
		if partition == 0 {
			partition = key.TeamID
		}
		scopes = append(scopes, distconcurrency.Scope{Kind: "key", ID: key.ID, Limit: *key.MaxParallelRequests})
	}
	if key != nil && key.OwnerMaxParallelRequests != nil && *key.OwnerMaxParallelRequests > 0 {
		if partition == 0 {
			partition = key.TeamID
		}
		switch {
		case key.UserID != nil && *key.UserID > 0:
			scopes = append(scopes, distconcurrency.Scope{Kind: "user", ID: *key.UserID, Limit: *key.OwnerMaxParallelRequests})
		case key.ServiceAccountID != nil && *key.ServiceAccountID > 0:
			scopes = append(scopes, distconcurrency.Scope{Kind: "service_account", ID: *key.ServiceAccountID, Limit: *key.OwnerMaxParallelRequests})
		}
	}
	return partition, scopes
}

func newTenantLeaseMember() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("fallback-%d-%d", time.Now().UnixNano(), tenantRequestSequence.Add(1))
}

func concurrencyLeaseTTL(requestDeadline time.Duration) time.Duration {
	const recoveryBuffer = 30 * time.Second
	const minimum = 2 * time.Minute
	ttl := requestDeadline + recoveryBuffer
	if ttl < minimum {
		return minimum
	}
	return ttl
}

func retryAfterSeconds(wait time.Duration) string {
	seconds := wait / time.Second
	if wait%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("%d", seconds)
}

func writeConcurrencyError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":{"message":%q,"type":%q,"param":null,"code":%q}}`, message, code, code)
}

// limitBody returns a middleware that wraps r.Body in MaxBytesReader so
// any io.ReadAll downstream is bounded. Different limits per route class:
// /v1 carries chat payloads (large), /admin/auth/me JSON is small.
func limitBody(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				requestLimit := limit
				if limit == bodyLimitV1 && (r.URL.Path == "/v1/audio/transcriptions" || r.URL.Path == "/v1/audio/translations") {
					requestLimit = 32 << 20
				}
				r.Body = http.MaxBytesReader(w, r.Body, requestLimit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

const (
	bodyLimitV1    = int64(8 << 20) // 8 MiB — chat payloads + image inputs
	bodyLimitAdmin = int64(1 << 20) // 1 MiB — admin JSON
)

// startProviderHealthSampler captures one row per registered deployment
// every 30s into provider_health_samples. The sample combines the
// router's local breaker view with the store's 5m usage_log rollup,
// giving the Settings → Providers card both real-time and trend data.
// Prunes rows older than 7 days hourly.
func startProviderHealthSampler(ctx context.Context, st *store.Store, reg *router.Registry, tel *telemetry.Collector, logger *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		sampleTicker := time.NewTicker(30 * time.Second)
		pruneTicker := time.NewTicker(time.Hour)
		defer sampleTicker.Stop()
		defer pruneTicker.Stop()
		// First sample on boot so a fresh node has at least one data point
		// before the dashboard polls.
		sampleProviderHealth(ctx, st, reg, tel, logger)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sampleTicker.C:
				sampleProviderHealth(ctx, st, reg, tel, logger)
			case <-pruneTicker.C:
				cutoff := time.Now().Add(-7 * 24 * time.Hour)
				ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
				if n, err := st.PruneProviderHealthSamplesOlderThan(ctx2, cutoff); err != nil {
					logger.Warn("provider health prune", "err", err)
				} else if n > 0 {
					logger.Info("provider health prune", "removed", n)
				}
				cancel()
			}
		}
	}()
	return done
}

func sampleProviderHealth(ctx context.Context, st *store.Store, reg *router.Registry, tel *telemetry.Collector, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if reg == nil {
		return
	}
	stats5m := map[string]store.DeploymentStats5m{}
	if rows, err := st.GetDeploymentStats5m(ctx); err == nil {
		for _, s := range rows {
			stats5m[s.Deployment] = s
		}
	}
	for _, h := range reg.Health() {
		if ctx.Err() != nil {
			return
		}
		sample := store.ProviderHealthSample{
			DeploymentName:      h.Name,
			Ready:               h.Ready,
			Circuit:             h.Circuit,
			ConsecutiveFailures: h.ConsecutiveFailures,
		}
		if tel != nil {
			tel.SetBreakerOpen(h.Name, h.Circuit == "open")
		}
		if s, ok := stats5m[h.Name]; ok {
			rpm := s.RPM
			errPct := s.ErrorPct
			p50 := s.P50Ms
			p95 := s.P95Ms
			sample.RPM5m = &rpm
			sample.ErrorPct5m = &errPct
			sample.P50Ms = &p50
			sample.P95Ms = &p95
		}
		ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := st.InsertProviderHealthSample(ctx2, sample); err != nil {
			logger.Warn("provider health sample insert", "deployment", h.Name, "err", err)
		}
		cancel()
	}
}

// startPayloadRetention spawns a goroutine that prunes
// usage_log_payloads rows older than retention_hours every
// prune_interval_hours. Both default to sane values when unset, so
// configuring nothing still yields safe behavior (7-day retention,
// hourly sweeps).
func startPayloadRetention(ctx context.Context, st *store.Store, cfg config.PayloadsConfig, logger *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	retentionHours := cfg.RetentionHours
	if retentionHours <= 0 {
		retentionHours = 24 * 7
	}
	intervalHours := cfg.PruneIntervalHours
	if intervalHours <= 0 {
		intervalHours = 1
	}
	retention := time.Duration(retentionHours) * time.Hour
	interval := time.Duration(intervalHours) * time.Hour
	go func() {
		defer close(done)
		// Run once on boot so a long-stopped node catches up immediately.
		prune(ctx, st, retention, logger)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				prune(ctx, st, retention, logger)
			}
		}
	}()
	return done
}

func prune(ctx context.Context, st *store.Store, retention time.Duration, logger *slog.Logger) {
	cutoff := time.Now().Add(-retention)
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	n, err := st.PrunePayloadsOlderThan(pctx, cutoff)
	if err != nil {
		logger.Warn("payload retention prune failed", "err", err)
		return
	}
	if n > 0 {
		logger.Info("payload retention", "pruned", n, "cutoff", cutoff)
	}
}

// securityHeaders applies a baseline set of browser-protection headers
// to every response. The CSP is intentionally permissive enough that
// the embedded SPA still loads (script + style + img from self), and
// strict enough to neuter the obvious XSS pivot routes (no inline JS
// from untrusted sources, no framing). HSTS is only emitted on TLS so
// localhost over plain HTTP still works in dev.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		// Frame-ancestors and the rest live in CSP for forward-compat.
		// 'unsafe-inline' on style-src is needed by lucide-react's SVG
		// styling; revisit when the bundle is moved off it.
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// accessLog emits a structured slog line per request with method, path,
// status, byte count, latency, and request id.
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}

// spaHandler serves files from fsys, falling back to index.html for any path
// that isn't a real file (so client-side routing works).
func spaHandler(fsys fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(fsys))
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			fileServer.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(fsys, path); err != nil {
			data, e := fs.ReadFile(fsys, "index.html")
			if e != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		fileServer.ServeHTTP(w, r)
	}
}
