import type { Deployment, Pricing } from '../types'

// GateMux price list: model name → provider type and per-token USD costs.
// Provider identity is required so ambiguous names cannot cross-price models.
type PriceEntry = {
  input_cost_per_token: number
  output_cost_per_token: number
  cache_read_input_token_cost?: number
  cache_creation_input_token_cost?: number
  provider: string
}

export type PriceMatch = {
  provider_type: string
  upstream_model: string
  input_per_million_cents: number
  output_per_million_cents: number
  cache_read_per_million_cents: number | null
  cache_write_per_million_cents: number | null
  // new: no price yet; changed: differs from the stored price; same: no-op.
  status: 'new' | 'changed' | 'same'
}

// Per-token USD → whole cents per million tokens.
const toCentsPerMillion = (usd: number) => Math.round(usd * 1e8)

export function parsePriceList(text: string): Record<string, PriceEntry> {
  const data: unknown = JSON.parse(text)
  if (!data || typeof data !== 'object' || Array.isArray(data)) throw new Error('Expected a JSON object keyed by model name.')
  for (const [model, entry] of Object.entries(data)) {
    if (!model.trim() || !entry || typeof entry !== 'object' || Array.isArray(entry)) throw new Error('Each model must have a price object.')
    if (typeof entry.provider !== 'string' || !entry.provider.trim()) throw new Error(`Model "${model}" requires a provider field matching its deployment provider type.`)
    for (const field of ['input_cost_per_token', 'output_cost_per_token', 'cache_read_input_token_cost', 'cache_creation_input_token_cost']) {
      const rate: unknown = entry[field]
      if (rate === undefined && field.startsWith('cache_')) continue
      if (typeof rate !== 'number' || !Number.isFinite(rate) || rate < 0 || !Number.isSafeInteger(toCentsPerMillion(rate))) {
        throw new Error(`Model "${model}": ${field} must be a nonnegative, finite USD rate within the supported range.`)
      }
    }
  }
  return data as Record<string, PriceEntry>
}

// matchPrices finds a price for each deployment's provider and upstream
// model. Deployments the list doesn't cover (local models, custom names) are
// left out; they stay unpriced until someone prices them by hand.
export function matchPrices(list: Record<string, PriceEntry>, deployments: Deployment[], existing: Pricing[]): PriceMatch[] {
  const stored = new Map(existing.map((p) => [`${p.provider_type}\u0000${p.upstream_model}`, p]))
  const seen = new Set<string>()
  const out: PriceMatch[] = []
  for (const d of deployments) {
    const id = `${d.provider_type}\u0000${d.upstream_model}`
    if (seen.has(id)) continue
    seen.add(id)
    const candidates = [d.upstream_model, `${d.provider_type}/${d.upstream_model}`]
    const entry = candidates
      .map((k) => list[k])
      .find((e) => e?.provider === d.provider_type)
    if (!entry) continue
    const match: PriceMatch = {
      provider_type: d.provider_type,
      upstream_model: d.upstream_model,
      input_per_million_cents: toCentsPerMillion(entry.input_cost_per_token),
      output_per_million_cents: toCentsPerMillion(entry.output_cost_per_token),
      cache_read_per_million_cents: typeof entry.cache_read_input_token_cost === 'number' ? toCentsPerMillion(entry.cache_read_input_token_cost) : null,
      cache_write_per_million_cents: typeof entry.cache_creation_input_token_cost === 'number' ? toCentsPerMillion(entry.cache_creation_input_token_cost) : null,
      status: 'new',
    }
    const prev = stored.get(id)
    if (prev) {
      const same = prev.input_per_million_cents === match.input_per_million_cents
        && prev.output_per_million_cents === match.output_per_million_cents
        && (prev.cache_read_per_million_cents ?? null) === match.cache_read_per_million_cents
        && (prev.cache_write_per_million_cents ?? null) === match.cache_write_per_million_cents
      match.status = same ? 'same' : 'changed'
    }
    out.push(match)
  }
  return out
}

// unpricedDeployments lists enabled deployments with no stored price, one per
// provider and model; their requests are recorded as unpriced and left out
// of spend totals.
export function unpricedDeployments(deployments: Deployment[], existing: Pricing[]): Deployment[] {
  const seen = new Set(existing.map((p) => `${p.provider_type}\u0000${p.upstream_model}`))
  return deployments.filter((d) => {
    const id = `${d.provider_type}\u0000${d.upstream_model}`
    if (!d.enabled || seen.has(id)) return false
    seen.add(id)
    return true
  })
}
