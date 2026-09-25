import { useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Eraser, MessageSquare, Send, Square } from 'lucide-react'
import { api, ApiError } from '../api/client'
import { principalIsAdmin, principalIsManager, principalTeamSlug, type Principal } from '../auth'
import type { TeamModel } from '../types'
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  PageHeader,
  Section,
  Textarea,
  useToast,
} from '../components/ui'

// Playground is the in-UI try-it surface. It calls /v1/chat/completions
// directly with a key the operator pastes (we never get the secret value
// after issuance, so we can't auto-fill from the user's keys list — by
// design). We persist conversation history client-side only.
//
// Streaming uses the standard SSE shape since the gateway emits
// OpenAI-shaped chunks for every provider.

type Role = 'system' | 'user' | 'assistant'

interface Message {
  role: Role
  content: string
  meta?: ResponseMeta
}

interface ResponseMeta {
  deployment?: string
  modelUsed?: string
  promptTokens?: number
  completionTokens?: number
  latencyMs?: number
  cached?: boolean
}

const STORAGE_KEY = 'gatemux.playground'

export default function Playground({ principal }: { principal: Principal }) {
  const isAdmin = principalIsAdmin(principal)
  const ownTeam = principalIsManager(principal) ? principalTeamSlug(principal) : undefined
  const toast = useToast()
  const [aliases, setAliases] = useState<TeamModel[]>([])
  const [alias, setAlias] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [systemPrompt, setSystemPrompt] = useState('')
  const [temperature, setTemperature] = useState('0.7')
  const [maxTokens, setMaxTokens] = useState('512')
  const [stream, setStream] = useState(true)
  const [input, setInput] = useState('')
  const [messages, setMessages] = useState<Message[]>([])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const [params] = useSearchParams()

  // Restore non-secret session state from localStorage. Neither the API
  // key nor the conversation transcript is persisted — the key would
  // leak via XSS, and operators routinely paste sensitive system prompts
  // and PII into messages, so they stay in component memory only.
  useEffect(() => {
    try {
      const raw = window.localStorage.getItem(STORAGE_KEY)
      if (!raw) return
      const saved = JSON.parse(raw) as Partial<{
        alias: string
        systemPrompt: string
        temperature: string
        maxTokens: string
        stream: boolean
      }>
      if (saved.alias) setAlias(saved.alias)
      if (saved.systemPrompt) setSystemPrompt(saved.systemPrompt)
      if (saved.temperature) setTemperature(saved.temperature)
      if (saved.maxTokens) setMaxTokens(saved.maxTokens)
      if (typeof saved.stream === 'boolean') setStream(saved.stream)
    } catch {
      // localStorage may be disabled or invalid JSON; ignore
    }
    // One-time cleanup: prior versions of this page persisted `messages`
    // under the same key. Strip them on first load so existing operators
    // don't keep leaking transcripts after the upgrade.
    try {
      const raw = window.localStorage.getItem(STORAGE_KEY)
      if (raw) {
        const parsed = JSON.parse(raw)
        if (parsed && typeof parsed === 'object' && 'messages' in parsed) {
          delete parsed.messages
          window.localStorage.setItem(STORAGE_KEY, JSON.stringify(parsed))
        }
      }
    } catch {
      // ignore
    }
  }, [])

  useEffect(() => {
    const saved = { alias, systemPrompt, temperature, maxTokens, stream }
    try {
      window.localStorage.setItem(STORAGE_KEY, JSON.stringify(saved))
    } catch {
      // ignore
    }
  }, [alias, systemPrompt, temperature, maxTokens, stream])

  useEffect(() => {
    if (!isAdmin && !ownTeam) return
    let active = true
    const catalog = isAdmin ? api.listAliases({ limit: 200 }) : api.listTeamModels(ownTeam!, { limit: 200 })
    catalog
      .then((p) => {
        if (!active) return
        setAliases(p.items)
        // Default to an alias that can actually route: one with no
        // deployments fails every request, a poor first message.
        const routable = p.items.filter((a) => !('deployments' in a) || ((a as { deployments?: string[] }).deployments ?? []).length > 0)
        const fromUrl = params.get('alias')
        if (fromUrl) setAlias(fromUrl)
        else if (!alias && routable.length > 0) setAlias(routable[0].alias)
      })
      .catch((e) => { if (active) toast.error('Failed to load aliases', e instanceof ApiError ? e.message : undefined) })
    return () => { active = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isAdmin, ownTeam])

  // "Open in Playground" from a request log passes ?from=<usage id>. When
  // the body was captured, prefill its system prompt and last user message.
  useEffect(() => {
    const from = Number(params.get('from'))
    if (!isAdmin || !from) return
    api.getUsagePayload(from)
      .then((p) => {
        const body = p.request_body as { messages?: { role?: string; content?: unknown }[] } | null
        const msgs = Array.isArray(body?.messages) ? body!.messages! : []
        const text = (m?: { content?: unknown }) => (typeof m?.content === 'string' ? m.content : '')
        const sys = msgs.find((m) => m.role === 'system')
        const lastUser = [...msgs].reverse().find((m) => m.role === 'user')
        if (sys) setSystemPrompt(text(sys))
        if (lastUser) setInput(text(lastUser))
        toast.success('Loaded the request from the log', 'Paste a key to send it again.')
      })
      .catch(() => toast.error('This request’s body was not captured', 'Only the alias was carried over.'))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' })
  }, [messages])

  const canSend = alias && apiKey && input.trim().length > 0 && !busy

  // Owns the in-flight request so Stop and unmount can cancel it — without
  // this, navigating away mid-stream left the SSE read loop and the
  // upstream request running.
  const abortRef = useRef<AbortController | null>(null)
  useEffect(() => () => abortRef.current?.abort(), [])

  const stop = () => abortRef.current?.abort()

  const reset = () => {
    setMessages([])
    setErr(null)
  }

  const send = async () => {
    if (!canSend) return
    setErr(null)
    const userMessage: Message = { role: 'user', content: input.trim() }
    const history = messages.concat(userMessage)
    setMessages(history)
    setInput('')
    setBusy(true)
    const startedAt = performance.now()

    const reqMessages: { role: Role; content: string }[] = []
    if (systemPrompt.trim()) reqMessages.push({ role: 'system', content: systemPrompt.trim() })
    for (const m of history) {
      reqMessages.push({ role: m.role, content: m.content })
    }
    const body = {
      model: alias,
      messages: reqMessages,
      temperature: Number(temperature),
      max_tokens: Number(maxTokens),
      stream,
    }

    const controller = new AbortController()
    abortRef.current = controller
    try {
      if (stream) {
        await streamRequest(apiKey, body, startedAt, setMessages, controller.signal)
      } else {
        await jsonRequest(apiKey, body, startedAt, setMessages, controller.signal)
      }
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') {
        // User pressed Stop (or navigated away): keep whatever streamed in,
        // drop an empty placeholder, no error banner.
        setMessages((cur) => cur.filter((m) => !(m.role === 'assistant' && m.content === '' && !m.meta)))
      } else {
        const msg = e instanceof Error ? e.message : 'Request failed'
        setErr(msg)
        // Drop the half-finished assistant placeholder if any.
        setMessages((cur) => cur.filter((m) => !(m.role === 'assistant' && m.content === '' && !m.meta)))
      }
    } finally {
      abortRef.current = null
      setBusy(false)
    }
  }

  const visibleAliases = useMemo(() => aliases.map((a) => a.alias), [aliases])

  return (
    <div className="space-y-6">
      <PageHeader
        title="Playground"
        description="Test an alias with a real key. Nothing is stored."
      />

      {/* Two-column on desktop: settings rail + chat thread */}
      <div className="grid gap-6 lg:grid-cols-[280px_minmax(0,1fr)]">
        <aside className="space-y-4">
          <Section title="Setup">
            <Card padded>
              <div className="space-y-3">
                <Field label="Alias" id="playground-alias" required hint="Enter an alias allowed by your key. Suggestions are limited to 200 models; the key's policy is enforced on every request.">
                  <input id="playground-alias" list="playground-models" value={alias} onChange={(e) => setAlias(e.target.value)} className="block h-9 w-full rounded-md border border-border-base bg-bg-surface px-3 text-sm" />
                  <datalist id="playground-models">
                    {visibleAliases.map((a) => (
                      <option key={a} value={a}>
                        {a}
                      </option>
                    ))}
                  </datalist>
                </Field>
                <Field
                  label="API key"
                  required
                  hint="Paste the secret you copied when issuing a key. Not stored."
                >
                  <input
                    type="password"
                    value={apiKey}
                    onChange={(e) => setApiKey(e.target.value)}
                    placeholder="gw-…"
                    className="block h-9 w-full rounded-md border border-border-base bg-bg-surface px-3 font-mono text-sm text-fg-base placeholder:text-fg-subtle outline-none focus:border-accent"
                  />
                </Field>
              </div>
            </Card>
          </Section>

          <Section title="Parameters">
            <Card padded>
              <div className="space-y-3">
                <Field label="System prompt" hint="Optional — prepended to every request">
                  <Textarea
                    value={systemPrompt}
                    onChange={(e) => setSystemPrompt(e.target.value)}
                    rows={3}
                    placeholder="You are a helpful assistant."
                    className="font-sans text-sm"
                  />
                </Field>
                <div className="grid grid-cols-2 gap-2">
                  <Field label="Temperature">
                    <input
                      type="number"
                      step="0.1"
                      min="0"
                      max="2"
                      value={temperature}
                      onChange={(e) => setTemperature(e.target.value)}
                      className="block h-9 w-full rounded-md border border-border-base bg-bg-surface px-3 text-sm text-fg-base focus:border-accent focus:outline-none"
                    />
                  </Field>
                  <Field label="Max tokens">
                    <input
                      type="number"
                      min="1"
                      max="32000"
                      value={maxTokens}
                      onChange={(e) => setMaxTokens(e.target.value)}
                      className="block h-9 w-full rounded-md border border-border-base bg-bg-surface px-3 text-sm text-fg-base focus:border-accent focus:outline-none"
                    />
                  </Field>
                </div>
                <label className="flex cursor-pointer items-center gap-2 text-sm text-fg-base">
                  <input
                    type="checkbox"
                    checked={stream}
                    onChange={(e) => setStream(e.target.checked)}
                    className="h-4 w-4 accent-accent"
                  />
                  Stream response
                </label>
              </div>
            </Card>
          </Section>
        </aside>

        <section className="flex h-[calc(100vh-13rem)] min-h-[480px] flex-col rounded-lg border border-border-base bg-bg-surface shadow-sm">
          <div ref={scrollRef} className="flex-1 space-y-4 overflow-y-auto px-5 py-5">
            {messages.length === 0 ? (
              <EmptyState
                icon={<MessageSquare className="h-5 w-5" />}
                title="Send your first message"
                description="Pick an alias and paste a key to start. Streaming chunks render live."
              />
            ) : (
              messages.map((m, i) => <Bubble key={i} m={m} />)
            )}
          </div>

          {err && (
            <div className="border-t border-border-subtle bg-danger-subtle/50 px-5 py-2.5 text-xs text-danger-fg">
              {err}
            </div>
          )}

          <div className="border-t border-border-subtle p-3">
            <div className="flex items-end gap-2">
              <Textarea
                value={input}
                onChange={(e) => setInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                    e.preventDefault()
                    send()
                  }
                }}
                rows={2}
                placeholder="Ask something… (⌘+↵ to send)"
                className="font-sans text-sm"
              />
              <div className="flex shrink-0 flex-col gap-2">
                {busy ? (
                  <Button
                    variant="danger"
                    onClick={stop}
                    leadingIcon={<Square className="h-3.5 w-3.5" />}
                  >
                    Stop
                  </Button>
                ) : (
                  <Button
                    onClick={send}
                    disabled={!canSend}
                    leadingIcon={<Send className="h-3.5 w-3.5" />}
                  >
                    Send
                  </Button>
                )}
                <Button
                  variant="ghost"
                  onClick={reset}
                  disabled={messages.length === 0}
                  leadingIcon={<Eraser className="h-3.5 w-3.5" />}
                >
                  Clear
                </Button>
              </div>
            </div>
          </div>
        </section>
      </div>
    </div>
  )
}

function Bubble({ m }: { m: Message }) {
  const isUser = m.role === 'user'
  const isSystem = m.role === 'system'
  if (isSystem) return null
  return (
    <div className={`flex ${isUser ? 'justify-end' : 'justify-start'}`}>
      <div
        className={
          'max-w-[85%] rounded-lg px-4 py-2.5 text-sm leading-relaxed ' +
          (isUser
            ? 'bg-accent text-accent-fg'
            : 'border border-border-base bg-bg-base text-fg-base')
        }
      >
        {!isUser && (
          <div className="mb-1 text-[12px] font-medium text-fg-subtle">Assistant</div>
        )}
        <div className="whitespace-pre-wrap">{m.content || (!isUser && <em className="text-fg-subtle">…</em>)}</div>
        {m.meta && (
          <div className="mt-2 flex flex-wrap gap-1.5 border-t border-border-subtle pt-2 text-[12px] text-fg-subtle">
            {m.meta.deployment && <Badge tone="neutral">deployment: {m.meta.deployment}</Badge>}
            {m.meta.modelUsed && <Badge tone="neutral">model: {m.meta.modelUsed}</Badge>}
            {m.meta.promptTokens != null && <Badge tone="neutral">in: {m.meta.promptTokens}t</Badge>}
            {m.meta.completionTokens != null && <Badge tone="neutral">out: {m.meta.completionTokens}t</Badge>}
            {m.meta.latencyMs != null && <Badge tone="neutral">{m.meta.latencyMs}ms</Badge>}
            {m.meta.cached && <Badge tone="success">cached</Badge>}
          </div>
        )}
      </div>
    </div>
  )
}

interface ChatBody {
  model: string
  messages: { role: Role; content: string }[]
  temperature: number
  max_tokens: number
  stream: boolean
}

async function jsonRequest(
  apiKey: string,
  body: ChatBody,
  startedAt: number,
  setMessages: React.Dispatch<React.SetStateAction<Message[]>>,
  signal: AbortSignal,
) {
  // credentials: 'omit' — the server prioritizes session cookies over the
  // Authorization header, so sending them would silently test the signed-in
  // account instead of the pasted key.
  const res = await fetch('/v1/chat/completions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${apiKey}` },
    body: JSON.stringify(body),
    credentials: 'omit',
    signal,
  })
  if (!res.ok) {
    const e = await res.json().catch(() => ({}))
    throw new Error(e?.error?.message ?? `HTTP ${res.status}`)
  }
  const data = await res.json()
  const text: string = data.choices?.[0]?.message?.content ?? ''
  const meta: ResponseMeta = {
    modelUsed: data.model,
    promptTokens: data.usage?.prompt_tokens,
    completionTokens: data.usage?.completion_tokens,
    latencyMs: Math.round(performance.now() - startedAt),
    cached: res.headers.get('X-Gatemux-Cache')?.startsWith('hit') ?? false,
  }
  setMessages((cur) => [...cur, { role: 'assistant', content: text, meta }])
}

async function streamRequest(
  apiKey: string,
  body: ChatBody,
  startedAt: number,
  setMessages: React.Dispatch<React.SetStateAction<Message[]>>,
  signal: AbortSignal,
) {
  // Append an empty assistant placeholder we'll fill chunk-by-chunk.
  setMessages((cur) => [...cur, { role: 'assistant', content: '' }])

  // credentials: 'omit' for the same reason as jsonRequest; the signal also
  // tears down the SSE read loop below when the user stops or navigates away.
  const res = await fetch('/v1/chat/completions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${apiKey}` },
    body: JSON.stringify(body),
    credentials: 'omit',
    signal,
  })
  if (!res.ok) {
    const e = await res.json().catch(() => ({}))
    throw new Error(e?.error?.message ?? `HTTP ${res.status}`)
  }
  if (!res.body) throw new Error('Streaming not supported by browser')

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let modelUsed: string | undefined
  let promptTokens: number | undefined
  let completionTokens: number | undefined

  while (true) {
    const { value, done } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const lines = buffer.split('\n')
    buffer = lines.pop() ?? ''
    for (const line of lines) {
      const trimmed = line.trim()
      if (!trimmed.startsWith('data:')) continue
      const data = trimmed.slice(5).trim()
      if (data === '[DONE]') continue
      try {
        const chunk = JSON.parse(data)
        modelUsed = modelUsed ?? chunk.model
        const delta = chunk.choices?.[0]?.delta?.content ?? ''
        if (chunk.usage) {
          promptTokens = chunk.usage.prompt_tokens
          completionTokens = chunk.usage.completion_tokens
        }
        if (delta) {
          setMessages((cur) => appendToLastAssistant(cur, delta))
        }
      } catch {
        // Skip malformed chunks; the gateway sometimes sends keep-alives
      }
    }
  }

  const meta: ResponseMeta = {
    modelUsed,
    promptTokens,
    completionTokens,
    latencyMs: Math.round(performance.now() - startedAt),
    cached: res.headers.get('X-Gatemux-Cache')?.startsWith('hit') ?? false,
  }
  setMessages((cur) => withReplyMeta(cur, meta))
}

function appendToLastAssistant(cur: Message[], delta: string): Message[] {
  if (cur.length === 0) return cur
  const last = cur[cur.length - 1]
  if (last.role !== 'assistant') return cur
  return [...cur.slice(0, -1), { ...last, content: last.content + delta }]
}

function withReplyMeta(cur: Message[], meta: ResponseMeta): Message[] {
  if (cur.length === 0) return cur
  const last = cur[cur.length - 1]
  if (last.role !== 'assistant') return cur
  return [...cur.slice(0, -1), { ...last, meta }]
}
