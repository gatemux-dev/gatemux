import { describe, expect, it } from 'vitest'
import { keyBudgetUSD, parseKeyBudgetUSD } from './keyBudget'

describe('key budget USD conversion', () => {
  it('converts exact cents without rounding and preserves the largest safe value', () => {
    for (const [text, cents] of [['0.29', 29], ['1.01', 101], ['25', 2500], ['90071992547409.91', Number.MAX_SAFE_INTEGER]] as const) {
      expect(parseKeyBudgetUSD(text)).toBe(cents)
      expect(parseKeyBudgetUSD(keyBudgetUSD(cents))).toBe(cents)
    }
    for (const text of ['', ' ', '0', '0.00']) expect(parseKeyBudgetUSD(text)).toBeNull()
  })
  it('rejects negatives, rounding, exponents, invalid and unsafe numbers', () => {
    for (const text of ['-1', '0.001', '1e2', 'NaN', 'Infinity', '12x', '1,000', '90071992547409.92', '9'.repeat(100)]) {
      expect(() => parseKeyBudgetUSD(text)).toThrow()
    }
  })
})
