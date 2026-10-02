// One money formatter for the console so budgets like 1000000000 cents render
// as $10,000,000.00 everywhere instead of an unbroken digit run.
export function fmtUSD(cents: number): string {
  return '$' + (cents / 100).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

const MICROCENTS_PER_DOLLAR = 100_000_000n
// Same grouping as toLocaleString('en-US'), built once: formatting is hot in tables.
const dollarGrouping = new Intl.NumberFormat('en-US')

// Exact dollars from micro-cents (1 µ¢ = 1/1,000,000 cent), as the API sends
// them: a base-10 string or a bigint. BigInt only, so values past 2^53 stay
// exact. At least 2 and at most 8 fraction digits, trailing zeros trimmed
// after the second, so a positive amount never renders as $0.00. Throws on
// invalid input; callers render that as "Unknown".
export function fmtMicrocents(value: string | bigint): string {
  let microcents: bigint
  if (typeof value === 'bigint') {
    microcents = value
  } else {
    if (!/^(0|[1-9][0-9]*)$/.test(value)) throw new Error(`invalid micro-cent amount: ${JSON.stringify(value)}`)
    microcents = BigInt(value)
  }
  if (microcents < 0n) throw new Error('negative micro-cent amount')
  const dollars = dollarGrouping.format(microcents / MICROCENTS_PER_DOLLAR)
  let fraction = (microcents % MICROCENTS_PER_DOLLAR).toString().padStart(8, '0').replace(/0+$/, '')
  fraction = fraction.padEnd(2, '0')
  return `$${dollars}.${fraction}`
}

// sumMicrocents adds API micro-cent strings exactly. Returns null when any
// value is missing or invalid, so callers show "Unknown" instead of a total.
export function sumMicrocents(values: Iterable<string | undefined | null>): bigint | null {
  let total = 0n
  for (const v of values) {
    if (v == null || !/^(0|[1-9][0-9]*)$/.test(v)) return null
    total += BigInt(v)
  }
  return total
}

// Formats a micro-cent string, rendering missing or invalid values as "Unknown".
export function fmtMicrocentsOr(value: string | bigint | null | undefined, fallback = 'Unknown'): string {
  if (value == null) return fallback
  try {
    return fmtMicrocents(value)
  } catch {
    return fallback
  }
}

// Exact remaining budget: a whole-cent limit minus micro-cents used, floored
// at zero. Null when either value is missing or invalid.
export function remainingMicrocents(limitCents: number | null | undefined, usedMicrocents: string | null | undefined): bigint | null {
  if (limitCents == null || !Number.isSafeInteger(limitCents) || limitCents < 0) return null
  const used = sumMicrocents([usedMicrocents])
  if (used === null) return null
  const remaining = BigInt(limitCents) * 1_000_000n - used
  return remaining > 0n ? remaining : 0n
}
