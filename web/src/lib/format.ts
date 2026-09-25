// One date vocabulary for every table: "Sep 23, 2026" for dates and
// "Sep 23, 4:14 PM" for timestamps (the year appears only when it isn't
// the current one). Locale-aware, never the ambiguous 9/23/2026.
export function fmtDate(value: string | Date): string {
  return new Date(value).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}

export function fmtDateTime(value: string | Date): string {
  const d = new Date(value)
  const sameYear = d.getFullYear() === new Date().getFullYear()
  return d.toLocaleString(undefined, {
    month: 'short', day: 'numeric', ...(sameYear ? {} : { year: 'numeric' }),
    hour: 'numeric', minute: '2-digit',
  })
}
