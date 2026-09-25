export type Team = {
  id: number
  slug: string
  name: string
  usd_limit_cents?: number
  period: string
  rpm?: number
  tpm?: number
  max_parallel_requests?: number
  allowed_models?: string[]
  capture_payloads?: boolean
  customer_registration?: 'optional' | 'required' | 'auto_create'
  stats?: { active_keys: number; members: number; period_spend_cents: number }
}

export type ApiKey = {
  id: number
  prefix: string
  name: string
  user_id?: number
  service_account_id?: number
  service_account_name?: string
  owner_user_email?: string
  owner_user_name?: string
  metadata: Record<string, unknown>
  allowed_models: string[]
  rpm?: number
  tpm?: number
  max_parallel_requests?: number
  usd_limit_cents?: number
  expires_at?: string
  created_at: string
  paused_at?: string
  revoked_at?: string
  last_used_at?: string
  rotated_from_key_id?: number
}

export type CreateKeyResponse = {
  key: string
  prefix: string
  name: string
  team_slug: string
}

export type UsageRow = {
	accounting_state?: 'legacy' | 'priced' | 'estimated' | 'unknown' | 'unpriced' | 'not_billable'
  id: number
  team_slug: string
  team_name?: string
  alias: string
  deployment_name: string
  provider_type?: string
  strategy?: string
  model_used: string
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_cents: number
  latency_ms: number
  queue_ms?: number
  upstream_ms?: number
  ttfb_ms?: number
  postprocess_ms?: number
  status_code: number
  error?: string
  ts: string
  request_id?: string
  user_id?: number
  user_email?: string
  key_id?: number
  key_prefix?: string
  key_name?: string
  customer_external_id?: string
  request_tags?: string[]
  cached?: boolean
  client_ip?: string
  has_payload?: boolean
  /** Served by a deployment other than the alias's primary. */
  fallback?: boolean
  /** Strongest guardrail decision recorded for the request, if any. */
  guardrail_decision?: 'block' | 'error' | 'unsupported' | 'redact' | 'flag'
}

export type UsagePayload = {
  usage_id: number
  request_body?: unknown
  response_body?: unknown
  captured_at: string
}

export type PolicyLayer = {
  label: string
  usd_limit_cents?: number
  spend_so_far_cents: number
  period?: string
  rpm?: number
  tpm?: number
  max_parallel_requests?: number
  allowed_models?: string[]
}

export type EffectivePolicy = {
  key_id: number
  key_prefix: string
  key_name: string
  owner_kind: string
  period_start: string
  period_end: string
  team: PolicyLayer
  owner?: PolicyLayer
  key: PolicyLayer
  effective_allowed_models: string[]
  notes?: string[]
}

export type Passthrough = {
  id: number
  name: string
  target_url: string
  auth_header?: string
  auth_value_env?: string
  auth_value_prefix?: string
  enabled: boolean
  created_at: string
}

export type ReplayResponse = {
  original_usage_id: number
  new_usage_id?: number
  status_code: number
  latency_ms: number
  request_id?: string
  response_body?: unknown
  endpoint_kind: 'chat' | 'embeddings' | string
  replayed: boolean
  reason?: string
}

export type UserRole = 'admin' | 'manager' | 'member'

export type TeamMember = {
  id: number
  email: string
  name: string
  role: UserRole
  last_login_at?: string
  disabled_at?: string
}

export type TeamModel = { alias: string }

export type User = {
  id: number
  email: string
  name: string
  team_id?: number
  team_slug?: string
  role: UserRole
  is_admin: boolean
  usd_limit_cents?: number
  max_parallel_requests?: number
  period: string
  last_login_at?: string
  created_at: string
  disabled_at?: string
  oidc_sub?: string
  role_managed_by_oidc?: boolean
}

export type Invite = {
  id: number
  prefix: string
  email?: string
  team_id?: number
  team_slug?: string
  role: string
  expires_at: string
  accepted_at?: string
  created_at: string
}

export type CreateInviteResponse = Invite & {
  token: string
  url: string
}

export type DeploymentInfo = {
  name: string
  type: string
  upstream_model: string
  base_url?: string
}

export type AliasInfo = {
  alias: string
  deployments: string[]
}

export type SpendProjection = {
  scope_type: string
  scope_id: number
  period_start: string
  period_end: string
  spend_so_far_cents: number
  projected_cents: number
  limit_cents?: number
  days_to_limit?: number
  on_track: 'above' | 'below' | 'on' | 'unknown'
  needs_more_data: boolean
}

export type ServiceAccount = {
  id: number
  team_slug: string
  name: string
  description?: string
  usd_limit_cents?: number
  period: string
  rpm?: number
  tpm?: number
  max_parallel_requests?: number
  created_at: string
  archived_at?: string
}

export type SessionRow = {
  token_hash_prefix: string
  created_at: string
  expires_at: string
  last_seen_at?: string
  ip?: string
  user_agent?: string
  current: boolean
}

export type AuditEventBrief = {
  when: string
  actor_id: string
  actor_type: string
  action: string
  resource_type: string
  resource_id: string
}

export type Info = {
  version: string
  deployments: DeploymentInfo[]
  aliases: AliasInfo[]
  counts: {
    teams: number
    users: number
    active_keys: number
    pending_invites: number
  }
  started_at?: string
  uptime_seconds?: number
  inflight?: number
  listen_addr?: string
  db_ok?: boolean
  redis_ok?: boolean
  master_key_suffix?: string
  last_config_change?: AuditEventBrief
}

export type DeploymentCapabilities = {
	responses?: boolean
  chat: boolean
  stream_chat: boolean
  embeddings: boolean
  moderation?: boolean
  rerank?: boolean
  images?: boolean
  audio_transcribe?: boolean
  audio_speech?: boolean
  messages_passthrough?: boolean
}

export type Deployment = {
	streaming?: StreamingPolicy | null
  managed_by?: 'config'
  name: string
  provider_type: string
  upstream_model: string
  credential_ref: string
  base_url?: string
  region?: string
  enabled: boolean
  has_credential: boolean
  max_parallel_requests?: number
  capabilities?: DeploymentCapabilities
}

export type StreamingPolicy = {
  first_event_timeout?: string
  idle_timeout?: string
  write_timeout?: string
  keepalive_interval?: string
}

export type DeploymentStats5m = {
  deployment: string
  requests: number
  errors: number
  error_pct: number
  rpm: number
  p50_ms: number
  p95_ms: number
}

export type ProviderHealth = {
  name: string
  provider_type: string
  enabled: boolean
  ready: boolean
  has_credential: boolean
  circuit: string
  consecutive_failures: number
  open_until?: string
  last_error?: string
  in_flight: number
  max_parallel_requests: number
  stats?: DeploymentStats5m
}

export type GuardrailPolicy = {
  name: string
  type: 'banned_terms'
  mode: 'block' | 'redact' | 'flag'
  phase: 'pre' | 'post' | 'both'
  terms: string[]
}

export type GuardrailCatalogRow = {
  scope_type: 'team' | 'alias'
  scope_id: string
  scope_label: string
  name: string
  mode: string
  hits_24h: number
  blocks_24h: number
  last_decision?: string
}

export type GuardrailAssignment = {
  scope_type: 'team' | 'alias'
  scope_id: string
  policies: GuardrailPolicy[] | null
}

export type AlertRule = {
  id: number
  name: string
  enabled: boolean
  scope_type: string
  scope_id?: number | null
  trigger_type: string
  threshold_options: Record<string, unknown>
  channel_type: string
  channel_target: string
  cooldown_seconds: number
  created_at: string
}

export type AlertEvent = {
  id: number
  rule_id: number
  rule_name: string
  fired_at: string
  payload: Record<string, unknown>
  delivery_status: string
  delivery_error?: string
}

export type AliasStrategy = 'priority' | 'tagged' | 'region' | 'cost' | 'latency'

export type Customer = {
  id: number
  external_id: string
  name: string
  max_parallel_requests?: number
  usd_limit_cents?: number
  period: string
  rpm?: number
  tpm?: number
  created_at: string
}

export type RoutingConcurrencyLimit = {
  id: number
  scope: 'model' | 'provider'
  subject: string
  max_parallel_requests: number | null
  updated_at: string
}

export type ModelAlias = {
  alias: string
  managed_by?: 'config'
  deployments: string[]
  cache_enabled: boolean
  cache_ttl_seconds: number
  strategy: AliasStrategy
  strategy_options: Record<string, unknown>
}

export type Pricing = {
	cache_read_per_million_cents?: number | null
	cache_write_per_million_cents?: number | null
	cache_write_1h_per_million_cents?: number | null
	reasoning_per_million_cents?: number | null
  provider_type: string
  upstream_model: string
  input_per_million_cents: number
  output_per_million_cents: number
  effective_at: string
}

export type SpendTotals = {
  requests: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_cents: number
}

export type SpendByAlias = SpendTotals & {
  alias: string
}

export type SpendReport = {
  team_slug?: string
  customer_external_id?: string
  user_id?: number
  from: string
  to: string
  total: SpendTotals
  aliases: SpendByAlias[]
  group_by?: string
  breakdown?: SpendGroup[]
}

export type SpendBucket = {
  bucket: string // ISO timestamp
  total: SpendTotals
  aliases: Record<string, number>
}

export type SpendTimeseries = {
  from: string
  to: string
  bucket: string
  series: SpendBucket[]
  aliases: string[]
}

export type UsageBucket = {
  bucket: string
  requests: number
  errors: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_cents: number
  latency_p50_ms: number
  latency_p95_ms: number
  cache_hits?: number
}

export type AuditEvent = {
  id: number
  actor_type: string
  actor_id: string
  action: string
  resource_type: string
  resource_id: string
  metadata: Record<string, unknown>
  created_at: string
}

export type AuthenticatedUser = {
  id: number
  email: string
  name: string
  role?: 'admin' | 'manager' | 'member'
  is_admin: boolean
  team_id?: number
  team_slug?: string
}

export type MyKey = {
  id: number
  prefix: string
  name: string
  metadata: Record<string, unknown>
  allowed_models: string[]
  rpm?: number
  tpm?: number
  expires_at?: string
  created_at: string
  paused_at?: string
  revoked_at?: string
  last_used_at?: string
}

export type MyUsageRow = {
  id: number
  team_slug: string
  alias: string
  deployment_name: string
  model_used: string
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cost_cents: number
  latency_ms: number
  status_code: number
  error?: string
  ts: string
}

export type MyBudget = {
  limit_cents?: number
  period: string
  window_start?: string
  window_end?: string
  used_cents: number
}

export type LoginResponse = {
  user: AuthenticatedUser
}

export type WhoamiResponse = {
  user?: AuthenticatedUser
  is_master_key: boolean
}

export type GetInviteResponse = {
  email?: string
  team_slug?: string
  role: string
  expires_at: string
}

export type AcceptInviteResponse = {
  token: string
  user: AuthenticatedUser
}

export type UsageFacets = { '2xx': number; '4xx': number; '5xx': number }

export type ConnectionTest = {
  ok: boolean
  operation: string
  latency_ms: number
  message: string
}

export type GuardrailTestResult = {
  decision: 'allow' | 'block' | 'redact' | 'flag'
  output: string
  results: { name: string; mode: string; phase: string; decision: string }[]
}

export type SpendGroup = SpendTotals & { key: string; label?: string }
