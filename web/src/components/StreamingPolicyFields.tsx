import type { StreamingPolicy } from '../types'

export default function StreamingPolicyFields({ value, onChange }: {
  value: StreamingPolicy | null
  onChange: (value: StreamingPolicy | null) => void
}) {
  const fields = [
    ['first_event_timeout', 'First event / byte timeout', '30s'],
    ['idle_timeout', 'Upstream idle timeout', '30s'],
    ['write_timeout', 'Downstream write timeout', '15s'],
    ['keepalive_interval', 'SSE keepalive interval', '0s'],
  ] as const
  return <fieldset className="rounded-lg border border-border-base p-3 space-y-3">
    <legend className="px-1 text-sm font-medium text-fg-base">Response lifecycle</legend>
    <label className="flex items-center gap-2 text-sm text-fg-base">
      <input type="checkbox" checked={value !== null} onChange={e => onChange(e.target.checked ? {} : null)} />
      Override gateway defaults for this deployment
    </label>
    {value !== null && <div className="grid gap-3 sm:grid-cols-2">
      {fields.map(([field, label, placeholder]) => <label key={field} className="text-xs text-fg-muted">
        {label}
        <input aria-label={label} value={value[field] ?? ''} placeholder={placeholder}
          onChange={e => onChange({ ...value, [field]: e.target.value })}
          className="mt-1 block w-full rounded-md border border-border-base bg-bg-base px-3 py-2 text-sm text-fg-base outline-none focus:border-accent" />
      </label>)}
    </div>}
    <p className="text-xs text-fg-subtle">Durations such as 500ms or 30s. Blank/0s timeouts inherit gateway defaults. Blank/0s keepalive disables comments. Keepalives start after the first event and do not extend request deadlines. The gateway total deadline always applies.</p>
  </fieldset>
}

export function normalizedStreamingPolicy(value: StreamingPolicy | null): StreamingPolicy | null {
  if (value === null) return null
  return Object.fromEntries(Object.entries(value).filter(([, duration]) => duration?.trim()).map(([field, duration]) => [field, duration?.trim()]))
}
