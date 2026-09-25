import { useState } from 'react'
import { api, ApiError } from '../api/client'
import { Button, Disclosure, Field, Input, Modal, ModalFooter } from './ui'
import type { ConnectionTest, StreamingPolicy } from '../types'
import StreamingPolicyFields, { normalizedStreamingPolicy } from './StreamingPolicyFields'

// Presets fill the fields most people get wrong: the provider type key,
// the conventional credential variable and an example model. Bedrock and
// Vertex need a region and are configured in the config file instead.
const PRESETS: { type: string; label: string; env: string; model: string; local?: boolean }[] = [
  { type: 'openai', label: 'OpenAI', env: 'OPENAI_API_KEY', model: 'gpt-4o-mini' },
  { type: 'anthropic', label: 'Anthropic', env: 'ANTHROPIC_API_KEY', model: 'claude-sonnet-4-5' },
  { type: 'azure_openai', label: 'Azure OpenAI', env: 'AZURE_OPENAI_API_KEY', model: 'your-deployment-name' },
  { type: 'gemini', label: 'Google Gemini', env: 'GEMINI_API_KEY', model: 'gemini-2.5-flash' },
  { type: 'mistral', label: 'Mistral', env: 'MISTRAL_API_KEY', model: 'mistral-small-latest' },
  { type: 'groq', label: 'Groq', env: 'GROQ_API_KEY', model: 'llama-3.3-70b-versatile' },
  { type: 'together', label: 'Together', env: 'TOGETHER_API_KEY', model: 'meta-llama/Llama-3.3-70B-Instruct-Turbo' },
  { type: 'fireworks', label: 'Fireworks', env: 'FIREWORKS_API_KEY', model: 'accounts/fireworks/models/llama-v3p3-70b-instruct' },
  { type: 'openrouter', label: 'OpenRouter', env: 'OPENROUTER_API_KEY', model: 'openai/gpt-4o-mini' },
  { type: 'cohere', label: 'Cohere', env: 'COHERE_API_KEY', model: 'command-r-plus' },
  { type: 'openai_compatible', label: 'OpenAI-compatible', env: '', model: 'your-model', local: true },
  { type: 'ollama', label: 'Ollama', env: '', model: 'llama3.2', local: true },
  { type: 'vllm', label: 'vLLM', env: '', model: 'meta-llama/Llama-3.1-8B-Instruct', local: true },
]

export default function CreateDeploymentModal({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: () => void
}) {
  const [name, setName] = useState('')
  const [providerType, setProviderType] = useState('openai')
  const [upstreamModel, setUpstreamModel] = useState('')
  const [credentialRef, setCredentialRef] = useState('OPENAI_API_KEY')
  const [baseURL, setBaseURL] = useState('')
  const [maxParallelRequests, setMaxParallelRequests] = useState('')
  const [streaming, setStreaming] = useState<StreamingPolicy | null>(null)
  const [supportsChat, setSupportsChat] = useState(true)
  const [supportsStreamChat, setSupportsStreamChat] = useState(true)
  const [supportsEmbeddings, setSupportsEmbeddings] = useState(false)
  const [supportsResponses, setSupportsResponses] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  // After creation the dialog turns into a connection check, so a typo in
  // the key variable or model name shows up now, not on the first request.
  const [created, setCreated] = useState<string | null>(null)
  const [testing, setTesting] = useState(false)
  const [test, setTest] = useState<ConnectionTest | null>(null)

  const preset = PRESETS.find((p) => p.type === providerType) ?? PRESETS[0]
  const credentialRequired = !preset.local

  const choosePreset = (type: string) => {
    const next = PRESETS.find((p) => p.type === type)!
    // Only replace the credential name if the user hasn't typed their own.
    if (!credentialRef || PRESETS.some((p) => p.env === credentialRef)) setCredentialRef(next.env)
    setProviderType(type)
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    if (!supportsChat && !supportsStreamChat && !supportsEmbeddings && !supportsResponses) {
      setErr('Select at least one capability.')
      setBusy(false)
      return
    }
    const parsedMaxParallel = maxParallelRequests.trim() === '' ? undefined : Number(maxParallelRequests)
    if (parsedMaxParallel !== undefined && (!Number.isInteger(parsedMaxParallel) || parsedMaxParallel <= 0)) {
      setErr('Max parallel requests must be a positive whole number.')
      setBusy(false)
      return
    }
    try {
      await api.createDeployment({
        streaming: normalizedStreamingPolicy(streaming),
        name: name.trim(),
        provider_type: providerType,
        upstream_model: upstreamModel.trim(),
        credential_ref: credentialRef.trim(),
        base_url: baseURL.trim() || undefined,
        max_parallel_requests: parsedMaxParallel,
        supports_chat: supportsChat,
        supports_responses: supportsResponses,
        supports_stream_chat: supportsStreamChat,
        supports_embeddings: supportsEmbeddings,
      })
      setCreated(name.trim())
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : 'Could not create the deployment.')
    } finally {
      setBusy(false)
    }
  }

  const runTest = async () => {
    if (!created) return
    setTesting(true)
    try {
      setTest(await api.testDeployment(created))
    } catch (e) {
      setTest({ ok: false, operation: 'setup', latency_ms: 0, message: e instanceof ApiError ? e.message : 'The test could not run.' })
    } finally {
      setTesting(false)
    }
  }

  if (created) {
    return (
      <Modal title="Deployment created" onClose={onCreated} dismissible={false}>
        <div className="space-y-4">
          <p className="text-[14px] text-fg-muted">
            <span className="mono text-fg-base">{created}</span> is saved. Send one small test request to confirm the credential, URL and model before routing traffic to it.
          </p>
          {test && <ConnectionResult result={test} />}
          <ModalFooter>
            <Button variant="ghost" onClick={onCreated}>{test?.ok ? 'Done' : 'Skip for now'}</Button>
            <Button onClick={runTest} loading={testing}>{test ? 'Test again' : 'Test connection'}</Button>
          </ModalFooter>
        </div>
      </Modal>
    )
  }

  return (
    <Modal title="Add a deployment" onClose={onClose} dismissible={false}>
      <form onSubmit={submit} className="space-y-5">
        <Field label="Provider">
          <div className="preset-grid" role="radiogroup" aria-label="Provider">
            {PRESETS.map((p) => (
              <button
                key={p.type}
                type="button"
                role="radio"
                aria-checked={providerType === p.type}
                className={'preset' + (providerType === p.type ? ' is-on' : '')}
                onClick={() => choosePreset(p.type)}
              >
                {p.label}
              </button>
            ))}
          </div>
        </Field>

        <Field label="Name" required hint="How this deployment appears in routing and logs.">
          <Input type="text" required value={name} onChange={(e) => setName(e.target.value)} placeholder={`${preset.type.replace(/_/g, '-')}-prod`} />
        </Field>

        <Field label="Upstream model" required hint="The model name exactly as the provider spells it.">
          <Input type="text" required value={upstreamModel} onChange={(e) => setUpstreamModel(e.target.value)} className="font-mono" placeholder={preset.model} />
        </Field>

        <Field
          label={credentialRequired ? 'API key variable' : 'API key variable (optional)'}
          required={credentialRequired}
          hint={<>The name of an environment variable on the gateway that holds the key, <strong className="text-fg-base">not the key itself</strong>.</>}
        >
          <Input type="text" required={credentialRequired} value={credentialRef} onChange={(e) => setCredentialRef(e.target.value)} className="font-mono" placeholder={preset.env || 'LOCAL_API_KEY'} />
        </Field>

        {preset.local && (
          <Field label="Base URL" required hint="Where the gateway reaches your server.">
            <Input type="text" required value={baseURL} onChange={(e) => setBaseURL(e.target.value)} className="font-mono" placeholder={providerType === 'ollama' ? 'http://ollama:11434/v1' : 'http://localhost:8000/v1'} />
          </Field>
        )}

        <Disclosure
          title="Capabilities"
          summary={[supportsChat && 'chat', supportsStreamChat && supportsChat && 'streaming', supportsResponses && 'responses', supportsEmbeddings && 'embeddings'].filter(Boolean).join(', ') || 'none'}
        >
          <p className="text-[13px] text-fg-subtle">The router only sends this deployment the request types it supports.</p>
          <div className="space-y-1">
            <Capability label="Chat completions" hint="POST /v1/chat/completions" checked={supportsChat} onChange={setSupportsChat} />
            <Capability label="Streaming chat" hint="Chat completions with stream=true" checked={supportsStreamChat} onChange={setSupportsStreamChat} disabled={!supportsChat} />
            <Capability label="Native Responses" hint="POST /v1/responses; the upstream must implement it natively" checked={supportsResponses} onChange={setSupportsResponses} />
            <Capability label="Embeddings" hint="POST /v1/embeddings" checked={supportsEmbeddings} onChange={setSupportsEmbeddings} />
          </div>
        </Disclosure>

        <Disclosure title="Advanced" summary="Base URL, concurrency, streaming timeouts">
          {!preset.local && (
            <Field label="Base URL override" hint="Only for proxies or regional endpoints.">
              <Input type="text" value={baseURL} onChange={(e) => setBaseURL(e.target.value)} className="font-mono" placeholder="Provider default" />
            </Field>
          )}
          <Field label="Max parallel requests" hint="Per gateway process. When full, requests go to fallbacks.">
            <Input type="number" min="1" step="1" value={maxParallelRequests} onChange={(e) => setMaxParallelRequests(e.target.value)} placeholder="Unlimited" />
          </Field>
          <StreamingPolicyFields value={streaming} onChange={setStreaming} />
        </Disclosure>

        {err && <p role="alert" className="text-[13px] text-danger-fg">{err}</p>}
        <ModalFooter>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button type="submit" loading={busy}>{busy ? 'Creating…' : 'Create deployment'}</Button>
        </ModalFooter>
      </form>
    </Modal>
  )
}

export function ConnectionResult({ result }: { result: ConnectionTest }) {
  return (
    <div className={'conn-result ' + (result.ok ? 'is-ok' : 'is-err')} role="status">
      <strong>{result.ok ? 'Connection works' : 'Connection failed'}</strong>
      <span>{result.message}</span>
      {result.latency_ms > 0 && <span className="muted">{result.latency_ms.toLocaleString()} ms, {result.operation}</span>}
    </div>
  )
}

function Capability({
  label, hint, checked, onChange, disabled,
}: {
  label: string
  hint: string
  checked: boolean
  onChange: (v: boolean) => void
  disabled?: boolean
}) {
  return (
    <label
      className={
        'flex cursor-pointer items-start gap-2 rounded px-2 py-1.5 text-sm transition ' +
        (disabled ? 'cursor-not-allowed opacity-50' : 'hover:bg-bg-raised')
      }
    >
      <input
        type="checkbox"
        checked={checked && !disabled}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        className="mt-1 h-3.5 w-3.5 accent-accent"
      />
      <div>
        <div className="text-fg-base">{label}</div>
        <div className="text-[12.5px] text-fg-subtle">{hint}</div>
      </div>
    </label>
  )
}
