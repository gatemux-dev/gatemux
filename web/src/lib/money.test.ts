import { describe, expect, it } from 'vitest'
import { fmtMicrocents, fmtMicrocentsOr, remainingMicrocents, sumMicrocents } from './money'

describe('fmtMicrocents', () => {
  // Table from specs/001-exact-subcent-costs/contracts/console-money.md.
  it.each([
    ['0', '$0.00'],
    ['1', '$0.00000001'],
    ['3000', '$0.00003'],
    ['1000000', '$0.01'],
    ['150000000', '$1.50'],
    ['1234560000', '$12.3456'],
    ['9223372036854775807', '$92,233,720,368.54775807'],
  ])('formats %s µ¢ as %s', (input, want) => {
    expect(fmtMicrocents(input)).toBe(want)
    expect(fmtMicrocents(BigInt(input))).toBe(want)
  })

  it('never renders a positive amount as $0.00', () => {
    for (let i = 1; i <= 999999; i++) {
      if (fmtMicrocents(String(i)) === '$0.00') throw new Error(`${i} rendered as $0.00`)
    }
  }, 30_000)

  it.each(['-1', '1.5', '', ' 1', '1e3', 'abc'])('rejects invalid input %j', (input) => {
    expect(() => fmtMicrocents(input)).toThrow()
  })

  it('rejects a negative bigint', () => {
    expect(() => fmtMicrocents(-1n)).toThrow()
  })
})

describe('micro-cent helpers', () => {
  it('sums exactly past 2^53 and rejects invalid values', () => {
    expect(sumMicrocents(['9007199254740993', '1'])).toBe(9007199254740994n)
    expect(sumMicrocents(['1', 'x'])).toBeNull()
    expect(sumMicrocents([undefined])).toBeNull()
  })
  it('renders invalid amounts as Unknown', () => {
    expect(fmtMicrocentsOr('-1')).toBe('Unknown')
    expect(fmtMicrocentsOr(null)).toBe('Unknown')
    expect(fmtMicrocentsOr('3000')).toBe('$0.00003')
  })
  it('computes the exact remaining budget, floored at zero', () => {
    expect(remainingMicrocents(100, '99999000')).toBe(1000n)
    expect(remainingMicrocents(1, '2000000')).toBe(0n)
    expect(remainingMicrocents(undefined, '0')).toBeNull()
    expect(remainingMicrocents(1, 'bad')).toBeNull()
  })
})
