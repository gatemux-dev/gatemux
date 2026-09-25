package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gatemux-dev/gatemux/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
)

// Options configures the telemetry collector. Pass {} for the previous
// "stdout-only when env var set" behavior; populate fields from
// config.TelemetryConfig in production.
type Options struct {
	Registry       *prometheus.Registry // optional per-server registry; no global ownership
	ServiceName    string
	ServiceVersion string
	OTLPEndpoint   string
	OTLPProtocol   string  // "http" or "grpc"
	SampleRate     float64 // 0..1
	StdoutTracing  bool
}

type Collector struct {
	gatherer                   prometheus.Gatherer
	draining                   prometheus.Gauge
	requestsTotal              *prometheus.CounterVec
	requestLatency             *prometheus.HistogramVec
	inFlight                   prometheus.Gauge
	inferenceCost              *prometheus.CounterVec
	inferenceTokens            *prometheus.CounterVec
	rateLimitDenials           *prometheus.CounterVec
	budgetDenials              *prometheus.CounterVec
	upstreamLatency            *prometheus.HistogramVec
	upstreamAttempts           *prometheus.CounterVec
	cacheLookups               *prometheus.CounterVec
	breakerOpen                *prometheus.GaugeVec
	buildInfo                  *prometheus.GaugeVec
	inferenceRequests          *prometheus.CounterVec
	inferenceLatency           *prometheus.HistogramVec
	admissionInFlight          prometheus.Gauge
	admissionQueued            prometheus.Gauge
	admissionOutcomes          *prometheus.CounterVec
	admissionWait              *prometheus.HistogramVec
	deploymentInFlight         *prometheus.GaugeVec
	deploymentConcurrencyLimit *prometheus.GaugeVec
	deploymentSaturation       *prometheus.CounterVec
	tenantConcurrencyDecisions *prometheus.CounterVec
	guardrailDecisions         *prometheus.CounterVec
	guardrailLatency           *prometheus.HistogramVec
	accountingOperations       *prometheus.CounterVec
	accountingLatency          *prometheus.HistogramVec
	accountingRecovered        prometheus.Counter
	tracerProvider             *sdktrace.TracerProvider
}

// New is preserved for callers that only want metrics + stdout tracing.
// New callers should use NewWithOptions to pick exporter + sample rate.
func New(serviceName string) (*Collector, error) {
	return NewWithOptions(Options{
		ServiceName:   serviceName,
		StdoutTracing: config.Env("GATEMUX_OTEL_STDOUT") != "",
	})
}

// NewWithOptions builds the collector with explicit telemetry settings.
// Trace export is wired in this priority order:
//  1. OTLP endpoint (HTTP or gRPC) — for production / staging
//  2. stdout exporter — for local debugging
//  3. no exporter — spans still get created in-process so request_id
//     correlation works, but nothing leaves the binary
func NewWithOptions(opts Options) (*Collector, error) {
	reg := prometheus.DefaultRegisterer
	gatherer := prometheus.DefaultGatherer
	if opts.Registry != nil {
		reg, gatherer = opts.Registry, opts.Registry
		if err := reg.Register(prometheus.NewGoCollector()); err != nil {
			return nil, err
		}
		if err := reg.Register(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{})); err != nil {
			return nil, err
		}
	}
	c := &Collector{
		gatherer:             gatherer,
		draining:             prometheus.NewGauge(prometheus.GaugeOpts{Name: "gatemux_draining", Help: "One after this process starts irreversible drain; zero while accepting inference."}),
		accountingOperations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gatemux_accounting_operations_total", Help: "Durable completion/recovery operations by fixed operation and outcome."}, []string{"operation", "outcome"}),
		accountingLatency:    prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "gatemux_accounting_duration_seconds", Help: "Durable accounting database operation duration.", Buckets: prometheus.DefBuckets}, []string{"operation"}),
		accountingRecovered:  prometheus.NewCounter(prometheus.CounterOpts{Name: "gatemux_accounting_recovered_total", Help: "Interrupted intents durably reconciled by this process."}),
		guardrailDecisions:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gatemux_guardrail_decisions_total", Help: "Guardrail decisions, with fixed phase and outcome labels."}, []string{"phase", "decision"}),
		guardrailLatency:     prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "gatemux_guardrail_duration_seconds", Help: "Guardrail phase inspection time."}, []string{"phase"}),
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gatemux_http_requests_total",
			Help: "Total HTTP requests handled by GateMux.",
		}, []string{"method", "path", "status"}),
		requestLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gatemux_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "path", "status"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "gatemux_http_in_flight_requests",
			Help: "Current in-flight HTTP requests.",
		}),
		inferenceCost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gatemux_cost_cents_total",
			Help: "Total settled inference cost in cents.",
		}, []string{"alias", "provider"}),
		inferenceTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gatemux_tokens_total",
			Help: "Total inference tokens by alias and token type.",
		}, []string{"alias", "type"}),
		rateLimitDenials: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gatemux_rate_limit_denials_total",
			Help: "Total rate-limit denials.",
		}, []string{"scope", "metric"}),
		budgetDenials: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gatemux_budget_denials_total",
			Help: "Total budget denials.",
		}, []string{"scope"}),
		upstreamLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gatemux_upstream_request_duration_seconds",
			Help:    "Upstream provider request duration in seconds.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"provider", "deployment", "outcome"}),
		upstreamAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gatemux_upstream_attempts_total",
			Help: "Total upstream provider attempts (counts retries individually).",
		}, []string{"provider", "deployment", "outcome"}),
		cacheLookups: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gatemux_cache_lookups_total",
			Help: "Total prompt-cache lookups by outcome (hit, miss, bypass).",
		}, []string{"alias", "result"}),
		breakerOpen: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gatemux_circuit_breaker_open",
			Help: "1 when the per-deployment circuit breaker is open (failing fast), 0 when closed.",
		}, []string{"deployment"}),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gatemux_build_info",
			Help: "Build metadata. Always set to 1; values live in labels for grouping/joining.",
		}, []string{"version", "go_version"}),
		inferenceRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gatemux_inference_requests_total",
			Help: "Inference requests by alias, provider, and status (200, 4xx, 5xx, denied).",
		}, []string{"alias", "provider", "status"}),
		inferenceLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gatemux_inference_duration_seconds",
			Help:    "End-to-end inference duration (gateway-side) by alias, provider, and status.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"alias", "provider", "status"}),
		admissionInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "gatemux_admission_in_flight_requests",
			Help: "Inference and passthrough requests currently holding an admission slot.",
		}),
		admissionQueued: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "gatemux_admission_queued_requests",
			Help: "Inference and passthrough requests currently waiting for an admission slot.",
		}),
		admissionOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gatemux_admission_outcomes_total",
			Help: "Admission decisions by outcome (admitted, queue_full, queue_timeout, canceled).",
		}, []string{"outcome"}),
		admissionWait: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gatemux_admission_wait_duration_seconds",
			Help:    "Time spent waiting for an inference admission slot.",
			Buckets: []float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"outcome"}),
		deploymentInFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gatemux_deployment_in_flight_requests",
			Help: "Current upstream operations holding a local deployment concurrency slot.",
		}, []string{"deployment"}),
		deploymentConcurrencyLimit: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gatemux_deployment_concurrency_limit",
			Help: "Configured local deployment concurrency limit; 0 means unlimited.",
		}, []string{"deployment"}),
		deploymentSaturation: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gatemux_deployment_saturation_total",
			Help: "Total attempts skipped because a deployment had no local concurrency capacity.",
		}, []string{"deployment"}),
		tenantConcurrencyDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gatemux_tenant_concurrency_decisions_total",
			Help: "Distributed concurrency admission decisions by bounded scope and outcome.",
		}, []string{"scope", "outcome"}),
	}
	if err := reg.Register(c.requestsTotal); err != nil {
		return nil, err
	}
	if err := reg.Register(c.requestLatency); err != nil {
		return nil, err
	}
	if err := reg.Register(c.inFlight); err != nil {
		return nil, err
	}
	if err := reg.Register(c.inferenceCost); err != nil {
		return nil, err
	}
	if err := reg.Register(c.inferenceTokens); err != nil {
		return nil, err
	}
	if err := reg.Register(c.rateLimitDenials); err != nil {
		return nil, err
	}
	if err := reg.Register(c.budgetDenials); err != nil {
		return nil, err
	}
	if err := reg.Register(c.upstreamLatency); err != nil {
		return nil, err
	}
	if err := reg.Register(c.upstreamAttempts); err != nil {
		return nil, err
	}
	if err := reg.Register(c.cacheLookups); err != nil {
		return nil, err
	}
	if err := reg.Register(c.breakerOpen); err != nil {
		return nil, err
	}
	if err := reg.Register(c.buildInfo); err != nil {
		return nil, err
	}
	if err := reg.Register(c.inferenceRequests); err != nil {
		return nil, err
	}
	if err := reg.Register(c.inferenceLatency); err != nil {
		return nil, err
	}
	if err := reg.Register(c.admissionInFlight); err != nil {
		return nil, err
	}
	if err := reg.Register(c.admissionQueued); err != nil {
		return nil, err
	}
	if err := reg.Register(c.admissionOutcomes); err != nil {
		return nil, err
	}
	if err := reg.Register(c.admissionWait); err != nil {
		return nil, err
	}
	if err := reg.Register(c.deploymentInFlight); err != nil {
		return nil, err
	}
	if err := reg.Register(c.deploymentConcurrencyLimit); err != nil {
		return nil, err
	}
	if err := reg.Register(c.deploymentSaturation); err != nil {
		return nil, err
	}
	if err := reg.Register(c.tenantConcurrencyDecisions); err != nil {
		return nil, err
	}
	c.buildInfo.WithLabelValues(opts.ServiceVersion, runtimeGoVersion()).Set(1)
	if err := reg.Register(c.guardrailDecisions); err != nil {
		return nil, err
	}
	if err := reg.Register(c.guardrailLatency); err != nil {
		return nil, err
	}
	for _, metric := range []prometheus.Collector{c.accountingOperations, c.accountingLatency, c.accountingRecovered, c.draining} {
		if err := reg.Register(metric); err != nil {
			return nil, err
		}
	}

	serviceName := opts.ServiceName
	if serviceName == "" {
		serviceName = "gatemux"
	}
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(opts.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}

	tpOptions := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
	}

	// Sampler: explicit head sampler so traces don't accidentally swamp
	// the collector. SampleRate==0 falls through to NeverSample so the
	// SDK still tags requests with trace IDs we can stamp on logs, but
	// nothing gets exported.
	switch {
	case opts.SampleRate >= 1:
		tpOptions = append(tpOptions, sdktrace.WithSampler(sdktrace.AlwaysSample()))
	case opts.SampleRate <= 0:
		tpOptions = append(tpOptions, sdktrace.WithSampler(sdktrace.NeverSample()))
	default:
		tpOptions = append(tpOptions,
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(opts.SampleRate))),
		)
	}

	if opts.OTLPEndpoint != "" {
		exporter, err := newOTLPExporter(context.Background(), opts.OTLPEndpoint, opts.OTLPProtocol)
		if err != nil {
			return nil, fmt.Errorf("otlp exporter: %w", err)
		}
		tpOptions = append(tpOptions, sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(exporter)))
	}
	if opts.StdoutTracing {
		exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("stdout exporter: %w", err)
		}
		tpOptions = append(tpOptions, sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(exporter)))
	}

	tp := sdktrace.NewTracerProvider(tpOptions...)
	otel.SetTracerProvider(tp)
	c.tracerProvider = tp
	return c, nil
}

func (c *Collector) RecordAccounting(operation, outcome string, recovered int, duration time.Duration) {
	if c == nil || (operation != "completion" && operation != "recovery") || (outcome != "committed" && outcome != "failed") {
		return
	}
	c.accountingOperations.WithLabelValues(operation, outcome).Inc()
	c.accountingLatency.WithLabelValues(operation).Observe(duration.Seconds())
	if recovered > 0 && outcome == "committed" {
		c.accountingRecovered.Add(float64(recovered))
	}
}

func (c *Collector) SetDraining() {
	if c != nil {
		c.draining.Set(1)
	}
}

// newOTLPExporter parses the endpoint and dispatches to the right
// transport. We accept both bare host:port and full URL forms. http://
// and https:// trigger HTTP/protobuf; everything else is treated as gRPC.
func newOTLPExporter(ctx context.Context, endpoint, protocol string) (sdktrace.SpanExporter, error) {
	if protocol == "" {
		// Infer from endpoint shape if not set explicitly.
		if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
			protocol = "http"
		} else {
			protocol = "grpc"
		}
	}
	switch protocol {
	case "http":
		host, insecure, err := splitOTLPEndpoint(endpoint)
		if err != nil {
			return nil, err
		}
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(host)}
		if insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		return otlptracehttp.New(ctx, opts...)
	case "grpc":
		host, insecure, err := splitOTLPEndpoint(endpoint)
		if err != nil {
			return nil, err
		}
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(host)}
		if insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		return otlptracegrpc.New(ctx, opts...)
	default:
		return nil, fmt.Errorf("unsupported otlp_protocol %q (want http or grpc)", protocol)
	}
}

func splitOTLPEndpoint(endpoint string) (host string, insecure bool, err error) {
	if strings.Contains(endpoint, "://") {
		u, perr := url.Parse(endpoint)
		if perr != nil {
			return "", false, fmt.Errorf("parse otlp endpoint: %w", perr)
		}
		return u.Host, u.Scheme == "http", nil
	}
	// Bare host:port — assume insecure (local collector / sidecar).
	return endpoint, true, nil
}

func (c *Collector) Shutdown(ctx context.Context) error {
	if c == nil || c.tracerProvider == nil {
		return nil
	}
	return c.tracerProvider.Shutdown(ctx)
}

func (c *Collector) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(c.gatherer, promhttp.HandlerOpts{})
}

func (c *Collector) Middleware(next http.Handler) http.Handler {
	if c == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.inFlight.Inc()
		defer c.inFlight.Dec()

		tracer := otel.Tracer("gatemux/http")
		ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path)
		defer span.End()

		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r.WithContext(ctx))

		// Use chi's matched route pattern as the metric label, not the
		// concrete URL. Otherwise every {slug} / {id} / {name} burns a
		// new time-series and the Prometheus store explodes. chi
		// populates RoutePattern after ServeHTTP runs.
		route := "unknown"
		if rc := chi.RouteContext(r.Context()); rc != nil {
			if p := rc.RoutePattern(); p != "" {
				route = p
			}
		}
		status := strconv.Itoa(ww.Status())
		labels := prometheus.Labels{
			"method": r.Method,
			"path":   route,
			"status": status,
		}
		c.requestsTotal.With(labels).Inc()
		c.requestLatency.With(labels).Observe(time.Since(start).Seconds())
		span.SetAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.route", route),
			attribute.String("http.target", r.URL.Path),
			attribute.Int("http.status_code", ww.Status()),
			attribute.String("request.id", middleware.GetReqID(r.Context())),
		)
		// Re-name the span now that we know the route pattern. Default
		// span name uses r.URL.Path which has the same cardinality issue.
		if route != "unknown" {
			span.SetName(r.Method + " " + route)
		}
	})
}

func (c *Collector) RecordInference(alias, provider string, promptTokens, completionTokens, totalTokens int, costCents int64) {
	if c == nil {
		return
	}
	if alias == "" {
		alias = "unknown"
	}
	if provider == "" {
		provider = "unknown"
	}
	if costCents > 0 {
		c.inferenceCost.WithLabelValues(alias, provider).Add(float64(costCents))
	}
	c.inferenceTokens.WithLabelValues(alias, "prompt").Add(float64(promptTokens))
	c.inferenceTokens.WithLabelValues(alias, "completion").Add(float64(completionTokens))
	c.inferenceTokens.WithLabelValues(alias, "total").Add(float64(totalTokens))
}

func (c *Collector) RecordRateLimit(scope, metric string) {
	if c == nil {
		return
	}
	c.rateLimitDenials.WithLabelValues(scope, metric).Inc()
}

func (c *Collector) RecordBudgetDeny(scope string) {
	if c == nil {
		return
	}
	if scope == "" {
		scope = "unknown"
	}
	c.budgetDenials.WithLabelValues(scope).Inc()
}

// RecordUpstream observes one upstream attempt: provider name (openai,
// anthropic, ...), deployment name, status_code (or 0 on transport error),
// and elapsed time. Outcome is "ok", "client_error", "server_error", or
// "transport_error" so dashboards can split retries from real failures.
func (c *Collector) RecordUpstream(provider, deployment string, statusCode int, elapsed time.Duration) {
	if c == nil {
		return
	}
	if provider == "" {
		provider = "unknown"
	}
	if deployment == "" {
		deployment = "unknown"
	}
	outcome := upstreamOutcome(statusCode)
	c.upstreamAttempts.WithLabelValues(provider, deployment, outcome).Inc()
	c.upstreamLatency.WithLabelValues(provider, deployment, outcome).Observe(elapsed.Seconds())
}

// RecordCacheLookup splits prompt-cache results so operators can chart
// hit-rate per alias. result is "hit", "miss", or "bypass".
func (c *Collector) RecordCacheLookup(alias, result string) {
	if c == nil {
		return
	}
	if alias == "" {
		alias = "unknown"
	}
	c.cacheLookups.WithLabelValues(alias, result).Inc()
}

// RecordInferenceRequest is the alias/provider-keyed counter+histogram
// pair that operator dashboards actually want. Different from the
// HTTP-shape requestsTotal/requestLatency pair (which is path-keyed) and
// from RecordInference (which only tracks tokens + cost). Call once per
// /v1 request after the response has been written so we get end-to-end
// gateway latency.
func (c *Collector) RecordInferenceRequest(alias, provider string, statusCode int, elapsed time.Duration) {
	if c == nil {
		return
	}
	if alias == "" {
		alias = "unknown"
	}
	if provider == "" {
		provider = "unknown"
	}
	status := strconv.Itoa(statusCode)
	c.inferenceRequests.WithLabelValues(alias, provider, status).Inc()
	c.inferenceLatency.WithLabelValues(alias, provider, status).Observe(elapsed.Seconds())
}

// RecordAdmission observes a completed admission decision and snapshots the
// bounded gate state. Outcome values are intentionally fixed and low-cardinality.
func (c *Collector) RecordAdmission(outcome string, waited time.Duration, inFlight, queued int64) {
	if c == nil {
		return
	}
	if outcome == "" {
		outcome = "unknown"
	}
	c.admissionOutcomes.WithLabelValues(outcome).Inc()
	c.admissionWait.WithLabelValues(outcome).Observe(waited.Seconds())
	c.SetAdmissionState(inFlight, queued)
}

func (c *Collector) SetAdmissionState(inFlight, queued int64) {
	if c == nil {
		return
	}
	c.admissionInFlight.Set(float64(inFlight))
	c.admissionQueued.Set(float64(queued))
}

// RecordDeploymentAdmission updates the deployment limiter gauges and records
// saturation without treating it as an upstream failure.
func (c *Collector) RecordDeploymentAdmission(deployment string, admitted bool, inFlight, limit int64) {
	if c == nil || deployment == "" {
		return
	}
	c.deploymentInFlight.WithLabelValues(deployment).Set(float64(inFlight))
	c.deploymentConcurrencyLimit.WithLabelValues(deployment).Set(float64(limit))
	if !admitted {
		c.deploymentSaturation.WithLabelValues(deployment).Inc()
	}
}

func (c *Collector) SetDeploymentConcurrency(deployment string, inFlight, limit int64) {
	if c == nil || deployment == "" {
		return
	}
	c.deploymentInFlight.WithLabelValues(deployment).Set(float64(inFlight))
	c.deploymentConcurrencyLimit.WithLabelValues(deployment).Set(float64(limit))
}

func (c *Collector) RecordTenantConcurrency(scope, outcome string) {
	if c == nil {
		return
	}
	switch scope {
	case "team", "key", "user", "service_account", "combined", "none":
	default:
		scope = "unknown"
	}
	switch outcome {
	case "admitted", "denied", "unavailable", "release_error":
	default:
		outcome = "unknown"
	}
	c.tenantConcurrencyDecisions.WithLabelValues(scope, outcome).Inc()
}

func (c *Collector) RecordGuardrail(phase, decision string, latency time.Duration) {
	if c == nil {
		return
	}
	if phase != "pre" && phase != "post" {
		phase = "validation"
	}
	switch decision {
	case "allow", "block", "redact", "flag", "unsupported", "error", "audit_error", "not_evaluated":
	default:
		decision = "error"
	}
	c.guardrailDecisions.WithLabelValues(phase, decision).Inc()
	c.guardrailLatency.WithLabelValues(phase).Observe(latency.Seconds())
}

// SetBreakerOpen toggles the per-deployment open/closed gauge.
// Called from the router when the breaker trips or recovers.
func (c *Collector) SetBreakerOpen(deployment string, open bool) {
	if c == nil {
		return
	}
	v := 0.0
	if open {
		v = 1.0
	}
	c.breakerOpen.WithLabelValues(deployment).Set(v)
}

func upstreamOutcome(status int) string {
	switch {
	case status == 0:
		return "transport_error"
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "client_error"
	default:
		return "ok"
	}
}

func runtimeGoVersion() string {
	return runtime.Version()
}
