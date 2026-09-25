import { getMasterKey, clearSession } from '../auth'
import type {
  Team,
  TeamMember,
  TeamModel,
  ApiKey,
  CreateKeyResponse,
  UsageRow,
  User,
  Invite,
  CreateInviteResponse,
  Info,
  Deployment,
  ProviderHealth,
  ModelAlias,
  RoutingConcurrencyLimit,
  Customer,
  Pricing,
  SpendReport,
  AuditEvent,
  AlertEvent,
  AlertRule,
  GuardrailAssignment,
  GuardrailTestResult,
  ConnectionTest,
  UsageFacets,
  GuardrailCatalogRow,
  GuardrailPolicy,
  ServiceAccount,
  SessionRow,
  SpendProjection,
  UsagePayload,
  ReplayResponse,
  EffectivePolicy,
  Passthrough,
  LoginResponse,
  WhoamiResponse,
  GetInviteResponse,
  AcceptInviteResponse,
  MyKey,
  MyUsageRow,
  MyBudget,
  SpendTimeseries,
  UsageBucket,
} from '../types'

export class ApiError extends Error {
  status: number
  type: string
  constructor(status: number, type: string, message: string) {
    super(message)
    this.status = status
    this.type = type
  }
}

// send is the single transport for every call: it attaches the master-key
// header (break-glass access has no cookie; account sessions ride on the
// HttpOnly cookie that credentials: 'same-origin' carries automatically),
// handles 401 sign-out, and parses error bodies. Public endpoints skip the
// stored credential and the auto-sign-out. An AbortSignal passed via init
// is forwarded to fetch.
async function send(
  path: string,
  init: RequestInit = {},
  opts: { public?: boolean } = {},
): Promise<Response> {
  const headers = new Headers(init.headers)
  const masterKey = opts.public ? null : getMasterKey()
  if (masterKey) headers.set('Authorization', `Bearer ${masterKey}`)
  if (init.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  // A browser may still have an expired or different account's HttpOnly
  // cookie. The server intentionally prioritizes cookies, so break-glass
  // requests must send only the explicitly selected master credential.
  const res = await fetch(path, {
    ...init,
    headers,
    credentials: init.credentials ?? (masterKey ? 'omit' : 'same-origin'),
  })
  if (!opts.public && res.status === 401) {
    // A late response from the previous authentication mode must not sign
    // out a master-key login completed while this request was in flight.
    if (getMasterKey() === masterKey) clearSession('expired')
    throw new ApiError(401, 'authentication_error', masterKey ? 'Admin master key was rejected. Sign in again.' : 'session expired')
  }
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as {
      error?: { message?: string; type?: string }
    }
    throw new ApiError(
      res.status,
      body.error?.type ?? 'error',
      body.error?.message ?? `HTTP ${res.status}`,
    )
  }
  return res
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await send(path, init)
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export interface Page<T> {
  items: T[]
  total: number
  limit: number
  offset: number
}

export interface PageQuery {
  limit?: number
  offset?: number
}

// requestPage wraps a paginated endpoint. The backend writes X-Total-Count
// on every list endpoint that calls setTotalCount; if it's missing we fall
// back to items.length so callers don't need to special-case unpaginated
// results.
/** Filters shared by the logs list and its facet counts. */
export interface UsageQuery {
  team?: string
  from?: string
  alias?: string
  key?: string
  status?: string
  latency?: string
  q?: string
}

function compactParams(values: Record<string, string | undefined>): URLSearchParams {
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(values)) if (v) params.set(k, v)
  return params
}

function usageParams(f: UsageQuery): URLSearchParams {
  return compactParams({ ...f, status: f.status === 'all' ? '' : f.status })
}

async function requestPage<T>(
  path: string,
  page?: PageQuery,
  extraParams?: URLSearchParams,
): Promise<Page<T>> {
  const params = extraParams ?? new URLSearchParams()
  const limit = page?.limit ?? 50
  const offset = page?.offset ?? 0
  params.set('limit', String(limit))
  params.set('offset', String(offset))
  const sep = path.includes('?') ? '&' : '?'
  const res = await send(`${path}${sep}${params.toString()}`)
  const items = (await res.json()) as T[]
  const totalHeader = res.headers.get('X-Total-Count')
  const total = totalHeader ? Number.parseInt(totalHeader, 10) : items.length
  return { items, total: Number.isFinite(total) ? total : items.length, limit, offset }
}

// Public requests don't inherit a stored bearer or auto-clear login on 401.
// Explicit credential validation can supply its own header without cookies.
async function publicRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  // /auth/login + /auth/logout need credentials so the Set-Cookie /
  // clear-cookie response is honored; send's same-origin default keeps
  // it scoped.
  const res = await send(path, init, { public: true })
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export const api = {
  whoami: () => request<WhoamiResponse>('/auth/me'),
  // Validate before persisting a key or changing the current principal. Neither
  // a stale cookie nor a live account session should affect master-key sign-in.
  verifyMasterKey: (key: string) => publicRequest<WhoamiResponse>('/auth/me', {
    headers: { Authorization: `Bearer ${key}` },
    credentials: 'omit',
  }),

  listMyKeys: (page?: PageQuery) => requestPage<MyKey>('/me/keys', page),
  listMyUsage: (page?: PageQuery) => requestPage<MyUsageRow>('/me/usage', page),
  getMyBudget: () => request<MyBudget>('/me/budget'),

  listMySessions: () => request<SessionRow[]>('/me/sessions'),
  revokeSession: (prefix: string) =>
    request<void>(`/me/sessions/${encodeURIComponent(prefix)}`, { method: 'DELETE' }),
  revokeOtherSessions: () =>
    request<{ revoked: number }>('/me/sessions', { method: 'DELETE' }),
  changePassword: (current_password: string, new_password: string) =>
    request<void>('/me/password', {
      method: 'POST',
      body: JSON.stringify({ current_password, new_password }),
    }),

  setUserDisabled: (userID: number, disabled: boolean) =>
    request<void>(`/admin/users/${userID}/disabled`, {
      method: 'PATCH',
      body: JSON.stringify({ disabled }),
    }),
  setUserRole: (userID: number, body: { role?: string; team_slug?: string }) =>
    request<void>(`/admin/users/${userID}/role`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),

  issuePasswordReset: (userID: number) =>
    request<{
      user_id: number
      email: string
      token: string
      url: string
      expires_at: string
    }>(`/admin/users/${userID}/password-reset`, { method: 'POST' }),
  getPasswordReset: (token: string) =>
    publicRequest<{ email: string; expires_at: string }>(
      `/reset/${encodeURIComponent(token)}`,
    ),
  consumePasswordReset: (token: string, new_password: string) =>
    publicRequest<void>(`/reset/${encodeURIComponent(token)}`, {
      method: 'POST',
      body: JSON.stringify({ new_password }),
    }),

  getProjection: (scopeType: 'team' | 'user', scopeID: number) => {
    const params = new URLSearchParams()
    params.set('scope_type', scopeType)
    params.set('scope_id', String(scopeID))
    return request<SpendProjection>(`/admin/projections?${params.toString()}`)
  },

  listServiceAccounts: (slug: string, page?: PageQuery) =>
    requestPage<ServiceAccount>(`/admin/teams/${encodeURIComponent(slug)}/service-accounts`, page),
  createServiceAccount: (slug: string, body: {
    name: string
    description?: string
    usd_limit_cents?: number | null
    period?: string
    rpm?: number | null
    tpm?: number | null
    max_parallel_requests?: number | null
  }) =>
    request<ServiceAccount>(`/admin/teams/${encodeURIComponent(slug)}/service-accounts`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  updateServiceAccount: (id: number, body: Record<string, unknown>) =>
    request<ServiceAccount>(`/admin/service-accounts/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),
  updateServiceAccountConcurrency: (id: number, maxParallelRequests: number | null) =>
    request<ServiceAccount>(`/admin/service-accounts/${id}/concurrency`, {
      method: 'PATCH',
      body: JSON.stringify({ max_parallel_requests: maxParallelRequests }),
    }),
  archiveServiceAccount: (id: number) =>
    request<void>(`/admin/service-accounts/${id}`, { method: 'DELETE' }),
  createServiceAccountKey: (id: number, body: {
    name?: string
    metadata?: Record<string, unknown>
    allowed_models?: string[]
    rpm?: number | null
    tpm?: number | null
    max_parallel_requests?: number | null
    usd_limit_cents?: number | null
    expires_at?: string | null
  }) =>
    request<CreateKeyResponse>(`/admin/service-accounts/${id}/keys`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  login: (email: string, password: string) =>
    publicRequest<LoginResponse>('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ email, password }),
    }),
  getLoginModes: () =>
    publicRequest<{
      master_key_enabled: boolean
      oidc_enabled?: boolean
      oidc_provider_name?: string
    }>('/auth/login-modes'),
  logout: () => request<void>('/auth/logout', { method: 'POST' }),

  getInvite: (token: string) =>
    publicRequest<GetInviteResponse>(`/invite/${encodeURIComponent(token)}`),
  acceptInvite: (token: string, body: { email: string; name?: string; password: string }) =>
    publicRequest<AcceptInviteResponse>(`/invite/${encodeURIComponent(token)}/accept`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  getInfo: () => request<Info>('/admin/info'),

  listTeams: (page?: PageQuery, query = '', opts: { stats?: boolean } = {}) =>
    requestPage<Team>('/admin/teams', page, compactParams({ q: query, stats: opts.stats ? '1' : '' })),
  getTeam: (slug: string) => request<Team>(`/admin/teams/${slug}`),
  listTeamMembers: (slug: string, page?: PageQuery, query = '') =>
    requestPage<TeamMember>(`/admin/teams/${encodeURIComponent(slug)}/members`, page, new URLSearchParams({ q: query })),
  listTeamModels: (slug: string, page?: PageQuery) =>
    requestPage<TeamModel>(`/admin/teams/${encodeURIComponent(slug)}/models`, page),
  createTeam: (body: {
    slug: string
    name?: string
    rpm?: number
    max_parallel_requests?: number
    usd_limit?: number
  }) =>
    request<Team>('/admin/teams', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  updateTeamBudget: (slug: string, body: { limit_cents?: number; period: string }) =>
    request<Team>(`/admin/teams/${slug}/budget`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  updateTeamConcurrency: (slug: string, maxParallelRequests: number | null) =>
    request<Team>(`/admin/teams/${slug}/concurrency`, {
      method: 'PATCH',
      body: JSON.stringify({ max_parallel_requests: maxParallelRequests }),
    }),
  updateTeamRates: (slug: string, body: { rpm: number | null; tpm: number | null }) =>
    request<Team>(`/admin/teams/${encodeURIComponent(slug)}/rates`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),
  updateTeamAllowedModels: (slug: string, allowedModels: string[]) =>
    request<Team>(`/admin/teams/${encodeURIComponent(slug)}/models`, {
      method: 'PUT',
      body: JSON.stringify({ allowed_models: allowedModels }),
    }),

  listKeys: (slug: string, page?: PageQuery) =>
    requestPage<ApiKey>(`/admin/teams/${slug}/keys`, page),
  createKey: (
    slug: string,
    body: {
      name?: string
      user_id?: number
      metadata?: Record<string, unknown>
      allowed_models?: string[]
      rpm?: number
      tpm?: number
      max_parallel_requests?: number
      usd_limit_cents?: number | null
      expires_at?: string
    },
  ) =>
    request<CreateKeyResponse>(`/admin/teams/${slug}/keys`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  updateKey: (
    id: number,
    body: {
      name?: string
      metadata?: Record<string, unknown>
      allowed_models?: string[]
      rpm?: number | null
      tpm?: number | null
      max_parallel_requests?: number | null
      usd_limit_cents?: number | null
      expires_at?: string | null
    },
  ) =>
    request<ApiKey>(`/admin/keys/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),
  rotateKey: (id: number, graceSeconds = 0) =>
    request<CreateKeyResponse>(`/admin/keys/${id}/rotate`, { method: 'POST', body: graceSeconds > 0 ? JSON.stringify({ grace_seconds: graceSeconds }) : undefined }),
  pauseKey: (id: number) => request<void>(`/admin/keys/${id}/pause`, { method: 'POST' }),
  resumeKey: (id: number) => request<void>(`/admin/keys/${id}/resume`, { method: 'POST' }),
  getKeyBudget: (id: number) => request<{ limit_cents?: number; period: string; window_start: string; window_end: string; used_cents: number }>(`/admin/keys/${id}/budget`),
  setKeyBudget: (id: number, usd_limit_cents: number | null) => request<void>(`/admin/keys/${id}/budget`, { method: 'PATCH', body: JSON.stringify({ usd_limit_cents }) }),
  revokeKey: (id: number) =>
    request<void>(`/admin/keys/${id}/revoke`, { method: 'POST' }),

  listUsage: (filter: UsageQuery = {}, page?: PageQuery) =>
    requestPage<UsageRow>('/admin/usage', page, usageParams(filter)),
  getUsageFacets: (filter: UsageQuery = {}) =>
    request<UsageFacets>(`/admin/usage/facets?${usageParams(filter)}`),
  getUsageRow: (id: number) => request<UsageRow>(`/admin/usage/${id}`),

  listUsers: (page?: PageQuery, filter: { q?: string; role?: string } = {}) =>
    requestPage<User>('/admin/users', page, compactParams(filter)),
  updateUserBudget: (id: number, body: { limit_cents?: number; period: string }) =>
    request<User>(`/admin/users/${id}/budget`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  updateUserConcurrency: (id: number, maxParallelRequests: number | null) =>
    request<User>(`/admin/users/${id}/concurrency`, {
      method: 'PATCH',
      body: JSON.stringify({ max_parallel_requests: maxParallelRequests }),
    }),

  listInvites: (page?: PageQuery) => requestPage<Invite>('/admin/invites', page),
  createInvite: (body: {
    email?: string
    team_slug?: string
    role: string
    expires_in_hours?: number
  }) =>
    request<CreateInviteResponse>('/admin/invites', {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  listDeployments: (page?: PageQuery, q = '') =>
    requestPage<Deployment>('/admin/deployments', page, compactParams({ q })),
  testDeployment: (name: string) =>
    request<ConnectionTest>(`/admin/deployments/${encodeURIComponent(name)}/test`, { method: 'POST', body: '{}' }),
  listProviderHealth: () => request<ProviderHealth[]>('/admin/provider-health'),
  createDeployment: (body: {
	streaming?: Deployment['streaming']
    name: string
    provider_type: string
    upstream_model: string
    credential_ref: string
    base_url?: string
    region?: string
    max_parallel_requests?: number
    supports_chat?: boolean
	 supports_responses?: boolean
    supports_stream_chat?: boolean
    supports_embeddings?: boolean
  }) =>
    request<Deployment>('/admin/deployments', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  updateDeployment: (
    name: string,
    body: {
	  streaming?: Deployment['streaming']
      provider_type?: string
      upstream_model?: string
      credential_ref?: string
      base_url?: string
      region?: string
      max_parallel_requests?: number
      supports_chat?: boolean
	   supports_responses?: boolean
      supports_stream_chat?: boolean
      supports_embeddings?: boolean
    },
  ) =>
    request<Deployment>(`/admin/deployments/${encodeURIComponent(name)}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),
  deleteDeployment: (name: string) =>
    request<void>(`/admin/deployments/${encodeURIComponent(name)}`, {
      method: 'DELETE',
    }),

  listAliases: (page?: PageQuery, filter: { q?: string; unrouted?: boolean } = {}) =>
    requestPage<ModelAlias>('/admin/aliases', page, compactParams({ q: filter.q, unrouted: filter.unrouted ? 'true' : '' })),
  listRoutingConcurrencyLimits: () => request<RoutingConcurrencyLimit[]>('/admin/concurrency/routing'),
  listCustomers: (slug: string, page?: PageQuery) =>
    requestPage<Customer>(`/admin/teams/${encodeURIComponent(slug)}/customers`, page),
  createCustomer: (slug: string, body: { external_id: string; name?: string; max_parallel_requests?: number | null }) =>
    request<Customer>(`/admin/teams/${encodeURIComponent(slug)}/customers`, { method: 'POST', body: JSON.stringify(body) }),
  updateCustomer: (slug: string, externalID: string, body: { name?: string; usd_limit_cents?: number | null; period?: string; rpm?: number | null; tpm?: number | null }) =>
    request<Customer>(`/admin/teams/${encodeURIComponent(slug)}/customers/${encodeURIComponent(externalID)}`, { method: 'PATCH', body: JSON.stringify(body) }),
  setCustomerRegistration: (slug: string, mode: NonNullable<Team['customer_registration']>) =>
    request<Team>(`/admin/teams/${encodeURIComponent(slug)}/customer-policy`, { method: 'PATCH', body: JSON.stringify({ customer_registration: mode }) }),
  getCustomerBudget: (slug: string, externalID: string) =>
    request<{ used_cents: number; window_start: string; window_end: string; period: string; limit_cents?: number }>(`/admin/teams/${encodeURIComponent(slug)}/customers/${encodeURIComponent(externalID)}/budget`),
  setCustomerConcurrency: (slug: string, externalID: string, maxParallelRequests: number | null) =>
    request<Customer>(`/admin/teams/${encodeURIComponent(slug)}/customers/${encodeURIComponent(externalID)}/concurrency`, {
      method: 'PATCH', body: JSON.stringify({ max_parallel_requests: maxParallelRequests }),
    }),
  setRoutingConcurrencyLimit: (body: Pick<RoutingConcurrencyLimit, 'scope' | 'subject' | 'max_parallel_requests'>) =>
    request<RoutingConcurrencyLimit>('/admin/concurrency/routing', {
      method: 'PUT', body: JSON.stringify(body),
    }),
  upsertAlias: (body: { alias: string; deployments: string[] }) =>
    request<ModelAlias>('/admin/aliases', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  updateAliasCache: (
    alias: string,
    body: { cache_enabled: boolean; cache_ttl_seconds: number },
  ) =>
    request<{ cache_enabled: boolean; cache_ttl_seconds: number }>(
      `/admin/aliases/${encodeURIComponent(alias)}/cache`,
      { method: 'PATCH', body: JSON.stringify(body) },
    ),
  updateAliasStrategy: (
    alias: string,
    body: { strategy: string; strategy_options: Record<string, unknown> },
  ) =>
    request<{ strategy: string; strategy_options: Record<string, unknown> }>(
      `/admin/aliases/${encodeURIComponent(alias)}/strategy`,
      { method: 'PATCH', body: JSON.stringify(body) },
    ),
  deleteAlias: (alias: string) =>
    request<void>(`/admin/aliases/${encodeURIComponent(alias)}`, {
      method: 'DELETE',
    }),

  listPricing: (page?: PageQuery) => requestPage<Pricing>('/admin/pricing', page),
  upsertPricing: (body: {
	cache_read_per_million_cents?: number | null
	cache_write_per_million_cents?: number | null
	cache_write_1h_per_million_cents?: number | null
	reasoning_per_million_cents?: number | null
    provider_type: string
    upstream_model: string
    input_per_million_cents: number
    output_per_million_cents: number
  }) =>
    request<Pricing>('/admin/pricing', {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  getSpendReport: (query?: {
    group_by?: string
    team?: string
    customer?: string
    user_id?: string
    from?: string
    to?: string
  }) => {
    const params = new URLSearchParams()
    if (query?.group_by) params.set('group_by', query.group_by)
    if (query?.team) params.set('team', query.team)
    if (query?.customer) params.set('customer', query.customer)
    if (query?.user_id) params.set('user_id', query.user_id)
    if (query?.from) params.set('from', query.from)
    if (query?.to) params.set('to', query.to)
    const suffix = params.size > 0 ? `?${params.toString()}` : ''
    return request<SpendReport>(`/admin/spend${suffix}`)
  },

  getSpendTimeseries: (query?: {
    team?: string
    user_id?: string
    from?: string
    to?: string
    bucket?: 'day' | 'hour' | 'week'
  }) => {
    const params = new URLSearchParams()
    if (query?.team) params.set('team', query.team)
    if (query?.user_id) params.set('user_id', query.user_id)
    if (query?.from) params.set('from', query.from)
    if (query?.to) params.set('to', query.to)
    if (query?.bucket) params.set('bucket', query.bucket)
    const suffix = params.size > 0 ? `?${params.toString()}` : ''
    return request<SpendTimeseries>(`/admin/spend/timeseries${suffix}`)
  },

  getUsageAggregate: (query?: {
    team?: string
    alias?: string
    from?: string
    to?: string
    bucket?: 'day' | 'hour' | 'week'
  }) => {
    const params = new URLSearchParams()
    if (query?.team) params.set('team', query.team)
    if (query?.alias) params.set('alias', query.alias)
    if (query?.from) params.set('from', query.from)
    if (query?.to) params.set('to', query.to)
    if (query?.bucket) params.set('bucket', query.bucket)
    const suffix = params.size > 0 ? `?${params.toString()}` : ''
    return request<UsageBucket[]>(`/admin/usage/aggregate${suffix}`)
  },

  listAudit: (page?: PageQuery, filter: { q?: string; resource_type?: string; actor_type?: string } = {}) =>
    requestPage<AuditEvent>('/admin/audit', page, compactParams(filter)),
  getAuditFacets: () => request<{ resource_types: string[]; actor_types: string[] }>('/admin/audit/facets'),

  listGuardrails: () => request<GuardrailCatalogRow[]>('/admin/guardrails'),
  listGuardrailAssignments: () => request<GuardrailAssignment[]>('/admin/guardrails/assignments'),
  testGuardrails: (body: { text: string; phase: 'pre' | 'post'; policies?: GuardrailPolicy[]; scope_type?: string; scope_id?: string }) =>
    request<GuardrailTestResult>('/admin/guardrails/test', { method: 'POST', body: JSON.stringify(body) }),
  getGuardrailScope: (scope: string, subject: string) => request<GuardrailPolicy[]>(`/admin/guardrails/${encodeURIComponent(scope)}/${encodeURIComponent(subject)}`),
  setGuardrailScope: (scope: string, subject: string, expected: GuardrailPolicy[], policies: GuardrailPolicy[]) => request<GuardrailPolicy[]>(`/admin/guardrails/${encodeURIComponent(scope)}/${encodeURIComponent(subject)}`, { method: 'PUT', body: JSON.stringify({ expected, policies }) }),

  listAlertRules: () => request<AlertRule[]>('/admin/alerts'),
  createAlertRule: (body: {
    name: string
    scope_type: string
    scope_id?: number | null
    trigger_type: string
    threshold_options: Record<string, unknown>
    channel_type: string
    channel_target: string
    cooldown_seconds: number
  }) =>
    request<AlertRule>('/admin/alerts', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  updateAlertRule: (id: number, body: { enabled: boolean }) =>
    request<void>(`/admin/alerts/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),
  deleteAlertRule: (id: number) =>
    request<void>(`/admin/alerts/${id}`, { method: 'DELETE' }),
  listAlertEvents: (limit?: number) =>
    request<AlertEvent[]>(`/admin/alert-events${limit ? `?limit=${limit}` : ''}`),

  getUsagePayload: (id: number) =>
    request<UsagePayload>(`/admin/usage/${id}/payload`),
  replayUsage: (id: number) =>
    request<ReplayResponse>(`/admin/usage/${id}/replay`, { method: 'POST' }),
  getEffectivePolicy: (keyId: number) =>
    request<EffectivePolicy>(`/admin/keys/${keyId}/effective-policy`),

  listPassthroughs: (page?: PageQuery) => requestPage<Passthrough>('/admin/passthroughs', page),
  createPassthrough: (body: {
    name: string
    target_url: string
    auth_header?: string
    auth_value_env?: string
    auth_value_prefix?: string
    enabled?: boolean
  }) =>
    request<Passthrough>('/admin/passthroughs', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  updatePassthrough: (
    name: string,
    body: {
      target_url?: string
      auth_header?: string
      auth_value_env?: string
      auth_value_prefix?: string
      enabled?: boolean
    },
  ) =>
    request<Passthrough>(`/admin/passthroughs/${encodeURIComponent(name)}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),
  deletePassthrough: (name: string) =>
    request<void>(`/admin/passthroughs/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  setTeamCapturePayloads: (slug: string, on: boolean) =>
    request<{ capture_payloads: boolean }>(
      `/admin/teams/${encodeURIComponent(slug)}/capture-payloads`,
      { method: 'PATCH', body: JSON.stringify({ capture_payloads: on }) },
    ),
}
