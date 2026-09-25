// Decimal conversion without floating-point rounding or silent precision loss.
export function parseKeyBudgetUSD(value: string): number | null {
  const text = value.trim()
  if (text === '') return null
  if (!/^\d+(\.\d{1,2})?$/.test(text) || text.length > 20) {
    throw new Error('Enter a non-negative USD amount with at most two decimal places.')
  }
  const [whole, fraction = ''] = text.split('.')
  const cents = BigInt(whole) * 100n + BigInt(fraction.padEnd(2, '0'))
  if (cents > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error('The key budget is too large.')
  return cents === 0n ? null : Number(cents)
}

export function keyBudgetUSD(cents?: number | null): string {
  if (cents == null || cents === 0) return ''
  if (!Number.isSafeInteger(cents) || cents < 0) return String(cents / 100)
  return `${BigInt(cents) / 100n}.${String(BigInt(cents) % 100n).padStart(2, '0')}`
}
