// One money formatter for the console so budgets like 1000000000 cents render
// as $10,000,000.00 everywhere instead of an unbroken digit run.
export function fmtUSD(cents: number): string {
  return '$' + (cents / 100).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}
