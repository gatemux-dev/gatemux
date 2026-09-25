import { describe, expect, it } from 'vitest'
import { matchPrices, parsePriceList, unpricedDeployments } from './priceImport'
import type { Deployment, Pricing } from '../types'

const dep = (provider_type: string, upstream_model: string, enabled = true): Deployment => ({
  name: `${provider_type}-${upstream_model}`, provider_type, upstream_model, credential_ref: 'env:X', enabled, has_credential: true,
})

const list = parsePriceList(JSON.stringify({
  'gpt-4o-mini': { input_cost_per_token: 1.5e-7, output_cost_per_token: 6e-7, cache_read_input_token_cost: 7.5e-8, provider: 'openai' },
  'claude-x': { input_cost_per_token: 3e-6, output_cost_per_token: 1.5e-5, provider: 'anthropic' },
  'groq/llama-y': { input_cost_per_token: 5e-8, output_cost_per_token: 8e-8, provider: 'groq' },
  'shared-name': { input_cost_per_token: 1e-6, output_cost_per_token: 2e-6, provider: 'openai' },
}))

describe('matchPrices', () => {
  it('converts per-token USD to cents per million and marks status', () => {
    const existing: Pricing[] = [{ provider_type: 'anthropic', upstream_model: 'claude-x', input_per_million_cents: 300, output_per_million_cents: 1500, effective_at: '' }]
    const got = matchPrices(list, [dep('openai', 'gpt-4o-mini'), dep('anthropic', 'claude-x'), dep('groq', 'llama-y')], existing)
    expect(got).toEqual([
      expect.objectContaining({ upstream_model: 'gpt-4o-mini', input_per_million_cents: 15, output_per_million_cents: 60, cache_read_per_million_cents: 8, status: 'new' }),
      expect.objectContaining({ upstream_model: 'claude-x', status: 'same' }),
      expect.objectContaining({ upstream_model: 'llama-y', input_per_million_cents: 5, output_per_million_cents: 8, status: 'new' }),
    ])
  })

  it('does not price a model listed for a different provider', () => {
    expect(matchPrices(list, [dep('mistral', 'shared-name'), dep('openai_compatible', 'gpt-4o-mini')], [])).toEqual([])
  })

  it('reports each provider and model once', () => {
    expect(matchPrices(list, [dep('openai', 'gpt-4o-mini'), dep('openai', 'gpt-4o-mini')], [])).toHaveLength(1)
  })

  it('uses exact gateway provider types and provider-qualified names', () => {
    const prices = parsePriceList(JSON.stringify({
      'shared-name': { provider: 'openai', input_cost_per_token: 1e-6, output_cost_per_token: 2e-6 },
      'azure_openai/shared-name': { provider: 'azure_openai', input_cost_per_token: 3e-6, output_cost_per_token: 4e-6 },
      'openai_compatible/local': { provider: 'openai_compatible', input_cost_per_token: 0, output_cost_per_token: 0 },
    }))
    expect(matchPrices(prices, [dep('azure_openai', 'shared-name'), dep('openai_compatible', 'local')], [])).toEqual([
      expect.objectContaining({ provider_type: 'azure_openai', input_per_million_cents: 300 }),
      expect.objectContaining({ provider_type: 'openai_compatible', input_per_million_cents: 0 }),
    ])
  })
})

describe('unpricedDeployments', () => {
  it('lists each unpriced provider and model once, skipping disabled deployments', () => {
    const existing: Pricing[] = [{ provider_type: 'openai', upstream_model: 'a', input_per_million_cents: 1, output_per_million_cents: 1, effective_at: '' }]
    expect(unpricedDeployments([dep('openai', 'a'), dep('openai', 'b'), { ...dep('openai', 'b'), name: 'b2' }, dep('openai', 'c', false)], existing).map((d) => d.upstream_model)).toEqual(['b'])
  })
})

it('rejects a list that is not an object', () => {
  expect(() => parsePriceList('[]')).toThrow()
})

it.each([undefined, '', 12])('rejects missing or invalid provider identity: %s', (provider) => {
  expect(() => parsePriceList(JSON.stringify({ x: { provider, input_cost_per_token: 1e-6, output_cost_per_token: 2e-6 } }))).toThrow(/provider/)
})

it.each([-1, '0.01', null, 1e100])('rejects unsafe imported rates: %s', (rate) => {
  for (const field of ['input_cost_per_token', 'output_cost_per_token', 'cache_read_input_token_cost', 'cache_creation_input_token_cost']) {
    expect(() => parsePriceList(JSON.stringify({ x: { provider: 'openai', input_cost_per_token: 0, output_cost_per_token: 0, [field]: rate } }))).toThrow(/rate/)
  }
})

it('rejects nonfinite JSON numbers and malformed entries', () => {
  expect(() => parsePriceList('{"x":{"provider":"openai","input_cost_per_token":1e400,"output_cost_per_token":0}}')).toThrow(/rate/)
  for (const value of [null, [], 'price']) expect(() => parsePriceList(JSON.stringify({ x: value }))).toThrow(/price object/)
})

it('requires base rates and preserves explicit free cache tiers', () => {
  expect(() => parsePriceList('{"x":{"provider":"openai","input_cost_per_token":0}}')).toThrow(/output_cost_per_token/)
  const prices = parsePriceList('{"x":{"provider":"openai","input_cost_per_token":0,"output_cost_per_token":0,"cache_read_input_token_cost":0,"cache_creation_input_token_cost":0}}')
  expect(matchPrices(prices, [dep('openai', 'x')], [])).toEqual([
    expect.objectContaining({ cache_read_per_million_cents: 0, cache_write_per_million_cents: 0 }),
  ])
})
