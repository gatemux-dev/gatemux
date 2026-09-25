import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { CheckCircle2, ChevronRight, Copy, ExternalLink } from 'lucide-react'
import { api, ApiError } from '../api/client'
import type { CreateKeyResponse, Deployment, ModelAlias, Team } from '../types'
import {
  Badge,
  Button,
  Field,
  Input,
  PageHeader,
  Select,
  useToast,
} from '../components/ui'

type StepKey = 'deployment' | 'alias' | 'team' | 'key' | 'done'

const PROVIDER_TYPES = [
  'openai',
  'azure_openai',
  'anthropic',
  'mistral',
  'groq',
  'together',
  'fireworks',
  'openrouter',
  'cohere',
  'bedrock',
  'vertex',
  'gemini',
  'openai_compatible',
] as const

export default function Setup() {
  const nav = useNavigate()
  const toast = useToast()
  const [step, setStep] = useState<StepKey>('deployment')
  const [deployments, setDeployments] = useState<Deployment[]>([])
  const [aliases, setAliases] = useState<ModelAlias[]>([])
  const [teams, setTeams] = useState<Team[]>([])
  const [issuedKey, setIssuedKey] = useState<CreateKeyResponse | null>(null)
  const [loading, setLoading] = useState(true)

  const refresh = async () => {
    try {
      const [d, a, t] = await Promise.all([
        api.listDeployments({ limit: 100 }),
        api.listAliases({ limit: 100 }),
        api.listTeams({ limit: 100 }),
      ])
      setDeployments(d.items)
      setAliases(a.items)
      setTeams(t.items)
    } catch (e) {
      toast.error('Setup load failed', e instanceof ApiError ? e.message : undefined)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // On first paint, jump to the first incomplete step so a returning operator
  // doesn't have to click through completed work. Guarded by a ref so it
  // fires exactly once after the initial load — later refresh() calls must
  // not yank the operator back to an earlier step mid-flow.
  const autoAdvanced = useRef(false)
  useEffect(() => {
    if (loading || autoAdvanced.current) return
    autoAdvanced.current = true
    if (deployments.length === 0) setStep('deployment')
    else if (aliases.length === 0) setStep('alias')
    else if (teams.length === 0) setStep('team')
    else if (!issuedKey) setStep('key')
    else setStep('done')
  }, [loading, deployments, aliases, teams, issuedKey])

  const progress = useMemo<Record<StepKey, boolean>>(
    () => ({
      deployment: deployments.length > 0,
      alias: aliases.length > 0,
      team: teams.length > 0,
      key: issuedKey !== null,
      done: false,
    }),
    [deployments, aliases, teams, issuedKey],
  )

  return (
    <>
      <PageHeader
        title="Setup wizard"
        description="Deployment → alias → team → first key."
        actions={
          <Button variant="ghost" onClick={() => nav('/teams')} trailingIcon={<ChevronRight size={13} />}>
            Skip to dashboard
          </Button>
        }
      />

      <div className="data-card" style={{ padding: 12 }}>
        <ol className="setup-steps">
          <SetupNav step={1} label="Deployment" active={step === 'deployment'} done={progress.deployment} onJump={() => setStep('deployment')} />
          <SetupNav step={2} label="Alias" active={step === 'alias'} done={progress.alias} onJump={() => progress.deployment && setStep('alias')} disabled={!progress.deployment} />
          <SetupNav step={3} label="Team" active={step === 'team'} done={progress.team} onJump={() => progress.alias && setStep('team')} disabled={!progress.alias} />
          <SetupNav step={4} label="First key" active={step === 'key'} done={progress.key} onJump={() => progress.team && setStep('key')} disabled={!progress.team} />
        </ol>
      </div>

      {step === 'deployment' && (
        <DeploymentStep
          existing={deployments}
          onCreated={async () => {
            toast.success('Deployment added')
            await refresh()
            setStep('alias')
          }}
          onContinue={() => setStep('alias')}
        />
      )}
      {step === 'alias' && (
        <AliasStep
          deployments={deployments}
          existing={aliases}
          onCreated={async () => {
            toast.success('Alias created')
            await refresh()
            setStep('team')
          }}
          onContinue={() => setStep('team')}
        />
      )}
      {step === 'team' && (
        <TeamStep
          existing={teams}
          onCreated={async () => {
            toast.success('Team created')
            await refresh()
            setStep('key')
          }}
          onContinue={() => setStep('key')}
        />
      )}
      {step === 'key' && (
        <KeyStep
          teams={teams}
          aliases={aliases}
          onIssued={(k) => {
            setIssuedKey(k)
            setStep('done')
            toast.success('Key issued')
          }}
        />
      )}
      {step === 'done' && issuedKey && (
        <DoneStep
          keyResp={issuedKey}
          alias={(aliases.find((a) => (a.deployments ?? []).length > 0) ?? aliases[0])?.alias ?? 'your-alias'}
          onFinish={() => nav('/teams')}
        />
      )}
    </>
  )
}

function SetupNav({
  step,
  label,
  active,
  done,
  disabled,
  onJump,
}: {
  step: number
  label: string
  active: boolean
  done: boolean
  disabled?: boolean
  onJump: () => void
}) {
  return (
    <li>
      <button
        type="button"
        disabled={disabled}
        onClick={onJump}
        className="setup-step"
        data-active={active ? 'true' : undefined}
        data-done={done ? 'true' : undefined}
      >
        <span className="setup-step-num">{done ? <CheckCircle2 size={14} /> : step}</span>
        <span>{label}</span>
      </button>
    </li>
  )
}

function DeploymentStep({
  existing,
  onCreated,
  onContinue,
}: {
  existing: Deployment[]
  onCreated: () => void
  onContinue: () => void
}) {
  const toast = useToast()
  const [name, setName] = useState('')
  const [providerType, setProviderType] = useState<typeof PROVIDER_TYPES[number]>('openai')
  const [upstreamModel, setUpstreamModel] = useState('')
  const [credentialRef, setCredentialRef] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      await api.createDeployment({
        name,
        provider_type: providerType,
        upstream_model: upstreamModel,
        credential_ref: credentialRef,
        supports_chat: true,
        supports_stream_chat: true,
        supports_embeddings: false,
      })
      onCreated()
    } catch (e) {
      toast.error('Could not create deployment', e instanceof ApiError ? e.message : undefined)
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="data-card" style={{ padding: 18 }}>
      <h2 className="data-h" style={{ fontSize: 16 }}>1 · Add a deployment</h2>
      <p className="muted" style={{ marginTop: 4, marginBottom: 16 }}>
        A deployment is a single upstream provider × model. GateMux routes alias traffic to one or more deployments.
      </p>

      {existing.length > 0 && (
        <div className="muted" style={{ marginBottom: 16 }}>
          You already have {existing.length} deployment{existing.length === 1 ? '' : 's'}: {existing.slice(0, 3).map((d) => d.name).join(', ')}
          {existing.length > 3 && ' …'}
        </div>
      )}

      <form onSubmit={submit} className="grid gap-3 sm:grid-cols-2">
        <Field label="Deployment name" required hint="Internal label, e.g. openai-gpt4o-prod">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="openai-gpt4o-prod" required />
        </Field>
        <Field label="Provider type" required>
          <Select value={providerType} onChange={(e) => setProviderType(e.target.value as typeof PROVIDER_TYPES[number])}>
            {PROVIDER_TYPES.map((p) => (
              <option key={p} value={p}>{p}</option>
            ))}
          </Select>
        </Field>
        <Field label="Upstream model" required hint="Provider's model identifier, e.g. gpt-4o-mini">
          <Input value={upstreamModel} onChange={(e) => setUpstreamModel(e.target.value)} placeholder="gpt-4o-mini" required />
        </Field>
        <Field label="Credential ref" required hint="Secret name configured under credentials">
          <Input value={credentialRef} onChange={(e) => setCredentialRef(e.target.value)} placeholder="openai-prod" required />
        </Field>
        <div className="sm:col-span-2 flex items-center justify-between pt-2">
          <a className="muted inline-flex items-center gap-1" href="https://github.com/gatemux-dev/gatemux/blob/main/docs/deploy.md" target="_blank" rel="noreferrer" style={{ fontSize: 12 }}>
            How credentials work <ExternalLink size={11} />
          </a>
          <div className="flex gap-2">
            {existing.length > 0 && (
              <Button variant="ghost" type="button" onClick={onContinue}>Use existing</Button>
            )}
            <Button type="submit" loading={busy} trailingIcon={<ChevronRight size={13} />}>Save & continue</Button>
          </div>
        </div>
      </form>
    </section>
  )
}

function AliasStep({
  deployments,
  existing,
  onCreated,
  onContinue,
}: {
  deployments: Deployment[]
  existing: ModelAlias[]
  onCreated: () => void
  onContinue: () => void
}) {
  const toast = useToast()
  const [alias, setAlias] = useState('')
  const [picked, setPicked] = useState<string[]>(deployments[0] ? [deployments[0].name] : [])
  const [busy, setBusy] = useState(false)

  const toggle = (name: string) =>
    setPicked((cur) => (cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name]))

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (picked.length === 0) {
      toast.error('Pick at least one deployment')
      return
    }
    setBusy(true)
    try {
      await api.upsertAlias({ alias, deployments: picked })
      onCreated()
    } catch (e) {
      toast.error('Could not create alias', e instanceof ApiError ? e.message : undefined)
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="data-card" style={{ padding: 18 }}>
      <h2 className="data-h" style={{ fontSize: 16 }}>2 · Create an alias</h2>
      <p className="muted" style={{ marginTop: 4, marginBottom: 16 }}>
        Aliases are the model names your app sees. Pick the deployments that should serve traffic for this alias.
      </p>

      {existing.length > 0 && (
        <div className="muted" style={{ marginBottom: 16 }}>
          You already have {existing.length} alias{existing.length === 1 ? '' : 'es'}: {existing.slice(0, 3).map((a) => a.alias).join(', ')}
          {existing.length > 3 && ' …'}
        </div>
      )}

      <form onSubmit={submit} className="flex flex-col gap-3">
        <Field label="Alias name" required hint="What clients will pass as `model`. Common: gpt-4o, prod-chat">
          <Input value={alias} onChange={(e) => setAlias(e.target.value)} placeholder="gpt-4o" required />
        </Field>
        <Field label="Backing deployments" required>
          <div className="chip-well">
            {deployments.map((d) => {
              const on = picked.includes(d.name)
              return (
                <button
                  key={d.name}
                  type="button"
                  onClick={() => toggle(d.name)}
                  className={'chip' + (on ? ' is-on' : '')}
                  style={{ cursor: 'pointer' }}
                >
                  {d.name}
                </button>
              )
            })}
          </div>
        </Field>
        <div className="flex items-center justify-end gap-2 pt-2">
          {existing.length > 0 && (
            <Button variant="ghost" type="button" onClick={onContinue}>Use existing</Button>
          )}
          <Button type="submit" loading={busy} trailingIcon={<ChevronRight size={13} />}>Save & continue</Button>
        </div>
      </form>
    </section>
  )
}

function TeamStep({
  existing,
  onCreated,
  onContinue,
}: {
  existing: Team[]
  onCreated: () => void
  onContinue: () => void
}) {
  const toast = useToast()
  const [slug, setSlug] = useState('')
  const [name, setName] = useState('')
  const [usdLimit, setUsdLimit] = useState('')
  const [rpm, setRpm] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      await api.createTeam({
        slug,
        name: name || undefined,
        rpm: rpm ? Number(rpm) : undefined,
        usd_limit: usdLimit ? Number(usdLimit) : undefined,
      })
      onCreated()
    } catch (e) {
      toast.error('Could not create team', e instanceof ApiError ? e.message : undefined)
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="data-card" style={{ padding: 18 }}>
      <h2 className="data-h" style={{ fontSize: 16 }}>3 · Create a team</h2>
      <p className="muted" style={{ marginTop: 4, marginBottom: 16 }}>
        Teams own keys, budgets, and rate limits. You can add more later.
      </p>

      {existing.length > 0 && (
        <div className="muted" style={{ marginBottom: 16 }}>
          You already have {existing.length} team{existing.length === 1 ? '' : 's'}: {existing.slice(0, 3).map((t) => t.slug).join(', ')}
          {existing.length > 3 && ' …'}
        </div>
      )}

      <form onSubmit={submit} className="grid gap-3 sm:grid-cols-2">
        <Field label="Slug" required hint="URL-safe id, e.g. acme-prod">
          <Input value={slug} onChange={(e) => setSlug(e.target.value)} placeholder="acme-prod" required />
        </Field>
        <Field label="Display name">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Acme Production" />
        </Field>
        <Field label="Monthly budget (USD)" hint="Optional. Leave empty for unlimited.">
          <Input type="number" min="0" value={usdLimit} onChange={(e) => setUsdLimit(e.target.value)} placeholder="500" />
        </Field>
        <Field label="Requests / minute" hint="Optional cap.">
          <Input type="number" min="0" value={rpm} onChange={(e) => setRpm(e.target.value)} placeholder="600" />
        </Field>
        <div className="sm:col-span-2 flex items-center justify-end gap-2 pt-2">
          {existing.length > 0 && (
            <Button variant="ghost" type="button" onClick={onContinue}>Use existing</Button>
          )}
          <Button type="submit" loading={busy} trailingIcon={<ChevronRight size={13} />}>Save & continue</Button>
        </div>
      </form>
    </section>
  )
}

function KeyStep({
  teams,
  aliases,
  onIssued,
}: {
  teams: Team[]
  aliases: ModelAlias[]
  onIssued: (k: CreateKeyResponse) => void
}) {
  const toast = useToast()
  const [teamSlug, setTeamSlug] = useState(teams[0]?.slug ?? '')
  const [keyName, setKeyName] = useState('first-key')
  const [allowedModels, setAllowedModels] = useState<string[]>([])
  const [busy, setBusy] = useState(false)

  const toggleModel = (m: string) =>
    setAllowedModels((cur) => (cur.includes(m) ? cur.filter((n) => n !== m) : [...cur, m]))

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      const resp = await api.createKey(teamSlug, {
        name: keyName,
        allowed_models: allowedModels.length > 0 ? allowedModels : undefined,
      })
      onIssued(resp)
    } catch (e) {
      toast.error('Could not issue key', e instanceof ApiError ? e.message : undefined)
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="data-card" style={{ padding: 18 }}>
      <h2 className="data-h" style={{ fontSize: 16 }}>4 · Issue your first key</h2>
      <p className="muted" style={{ marginTop: 4, marginBottom: 16 }}>
        We'll mint a virtual key bound to this team. The full secret is shown once on the next screen — copy it then.
      </p>

      <form onSubmit={submit} className="grid gap-3 sm:grid-cols-2">
        <Field label="Team" required>
          <Select value={teamSlug} onChange={(e) => setTeamSlug(e.target.value)}>
            {teams.map((t) => (
              <option key={t.slug} value={t.slug}>{t.name || t.slug}</option>
            ))}
          </Select>
        </Field>
        <Field label="Key name">
          <Input value={keyName} onChange={(e) => setKeyName(e.target.value)} placeholder="first-key" />
        </Field>
        <Field label="Allowed models" hint={allowedModels.length === 0 ? 'None selected, so the key can call every alias.' : `${allowedModels.length} selected.`}>
          <div className="chip-well">
            {aliases.map((a) => {
              const on = allowedModels.includes(a.alias)
              return (
                <button
                  key={a.alias}
                  type="button"
                  onClick={() => toggleModel(a.alias)}
                  className={'chip' + (on ? ' is-on' : '')}
                  style={{ cursor: 'pointer' }}
                >
                  {a.alias}
                </button>
              )
            })}
          </div>
        </Field>
        <div className="sm:col-span-2 flex items-center justify-end pt-2">
          <Button type="submit" loading={busy} disabled={!teamSlug}>
            Issue key
          </Button>
        </div>
      </form>
    </section>
  )
}

type FirstRequest = { id: number; status: number; latency: number }

function DoneStep({ keyResp, alias, onFinish }: { keyResp: CreateKeyResponse; alias: string; onFinish: () => void }) {
  const toast = useToast()
  const [seen, setSeen] = useState<FirstRequest | null>(null)
  const [sending, setSending] = useState(false)
  const copy = async (text: string, what: string) => {
    await navigator.clipboard.writeText(text)
    toast.success(`${what} copied`)
  }

  const body = JSON.stringify({ model: alias, messages: [{ role: 'user', content: 'Say hello in five words.' }], max_tokens: 20 })
  const sample = `curl ${window.location.origin}/v1/chat/completions \\
  -H "Authorization: Bearer ${keyResp.key}" \\
  -H "Content-Type: application/json" \\
  -d '${body}'`

  // Watch the logs for this key's first request, however it was sent.
  useEffect(() => {
    if (seen) return
    let alive = true
    const check = async () => {
      try {
        const page = await api.listUsage({ key: keyResp.prefix, team: keyResp.team_slug }, { limit: 1 })
        const row = page.items[0]
        if (alive && row) setSeen({ id: row.id, status: row.status_code, latency: row.latency_ms })
      } catch { /* keep polling */ }
    }
    void check()
    const t = window.setInterval(check, 3000)
    return () => { alive = false; window.clearInterval(t) }
  }, [seen, keyResp.prefix, keyResp.team_slug])

  const sendTest = async () => {
    setSending(true)
    try {
      const resp = await fetch('/v1/chat/completions', {
        method: 'POST',
        headers: { Authorization: `Bearer ${keyResp.key}`, 'Content-Type': 'application/json' },
        body,
      })
      if (!resp.ok) toast.error(`The gateway returned ${resp.status}`, (await resp.text()).slice(0, 200))
    } catch (e) {
      toast.error('The request could not be sent', e instanceof Error ? e.message : undefined)
    } finally {
      setSending(false)
    }
  }

  return (
    <section className="data-card" style={{ padding: 18 }}>
      <div className="flex items-center justify-between">
        <h2 className="data-h" style={{ fontSize: 16 }}>You're set up</h2>
        <Badge tone="success">ready</Badge>
      </div>
      <p className="muted" style={{ marginTop: 4, marginBottom: 16 }}>
        Copy this key now. It's the only time the full secret is shown; the prefix is kept so you can identify it later.
      </p>
      <div className="data-card" style={{ padding: 12, marginBottom: 12, background: 'var(--bg-subtle)' }}>
        <div className="flex items-center justify-between gap-2">
          <code className="mono" style={{ fontSize: 13, wordBreak: 'break-all' }}>{keyResp.key}</code>
          <Button variant="ghost" leadingIcon={<Copy size={13} />} onClick={() => void copy(keyResp.key, 'Key')}>Copy</Button>
        </div>
        <div className="muted" style={{ marginTop: 6, fontSize: 12 }}>
          Prefix <span className="mono">{keyResp.prefix}</span>, team <span className="mono">{keyResp.team_slug}</span>
        </div>
      </div>
      <div className="flex items-center justify-between gap-2" style={{ marginTop: 16 }}>
        <h3 className="data-h" style={{ fontSize: 13 }}>Send your first request</h3>
        <Button variant="ghost" size="sm" leadingIcon={<Copy size={12} />} onClick={() => void copy(sample, 'Command')}>Copy command</Button>
      </div>
      <pre className="data-card" style={{ padding: 12, marginTop: 8, fontSize: 12, overflowX: 'auto', background: 'var(--bg-subtle)' }}>
        <code>{sample}</code>
      </pre>
      <div className="first-request" role="status">
        {seen ? (
          <>
            <span className={`pill pill-${seen.status < 300 ? 'ok' : seen.status < 500 ? 'warn' : 'err'} tnum`}>{seen.status}</span>
            <span>
              {seen.status < 300 ? 'First request received' : 'First request failed'} in {seen.latency.toLocaleString()} ms.{' '}
              <Link className="linkish" to={`/usage?request=${seen.id}`}>Open it in Logs</Link>
            </span>
          </>
        ) : (
          <>
            <span className="first-request-wait" aria-hidden="true" />
            <span className="muted">Waiting for a request with this key. Run the command above or send one from here.</span>
            <Button variant="ghost" size="sm" loading={sending} onClick={() => void sendTest()}>Send test request</Button>
          </>
        )}
      </div>
      <div className="flex items-center justify-end pt-3">
        <Button onClick={onFinish} trailingIcon={<ChevronRight size={13} />}>Done</Button>
      </div>
    </section>
  )
}
