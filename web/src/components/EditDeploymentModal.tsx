import { useState } from 'react'
import { api, ApiError } from '../api/client'
import type { Deployment, StreamingPolicy } from '../types'
import { Button, Field, Input, Modal, ModalFooter, Select } from './ui'
import StreamingPolicyFields, { normalizedStreamingPolicy } from './StreamingPolicyFields'

export default function EditDeploymentModal({
  deployment,
  onClose,
  onSaved,
}: {
  deployment: Deployment
  onClose: () => void
  onSaved: () => void
}) {
  const [providerType, setProviderType] = useState(deployment.provider_type)
  const [upstreamModel, setUpstreamModel] = useState(deployment.upstream_model)
  const [credentialRef, setCredentialRef] = useState(deployment.credential_ref)
  const [baseURL, setBaseURL] = useState(deployment.base_url ?? '')
  const [region, setRegion] = useState(deployment.region ?? '')
  const [streaming, setStreaming] = useState<StreamingPolicy | null>(deployment.streaming ?? null)
  const [maxParallelRequests, setMaxParallelRequests] = useState(
    deployment.max_parallel_requests?.toString() ?? '',
  )
  const initialCaps = deployment.capabilities
  const [supportsChat, setSupportsChat] = useState(initialCaps?.chat ?? true)
  const [supportsStreamChat, setSupportsStreamChat] = useState(
    initialCaps?.stream_chat ?? true,
  )
  const [supportsEmbeddings, setSupportsEmbeddings] = useState(
    initialCaps?.embeddings ?? false,
  )
  const [supportsResponses, setSupportsResponses] = useState(initialCaps?.responses ?? (initialCaps?.chat !== false && ['openai', 'openai_compatible'].includes(deployment.provider_type)))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const credentialRequired = !['openai_compatible', 'ollama', 'vllm'].includes(
    providerType,
  )

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr(null)
    if (!supportsChat && !supportsStreamChat && !supportsEmbeddings && !supportsResponses) {
      setErr('select at least one capability')
      setBusy(false)
      return
    }
    const parsedMaxParallel = maxParallelRequests.trim() === ''
      ? 0
      : Number(maxParallelRequests)
    if (!Number.isInteger(parsedMaxParallel) || parsedMaxParallel < 0) {
      setErr('max parallel requests must be a positive whole number or blank')
      setBusy(false)
      return
    }
    try {
      await api.updateDeployment(deployment.name, {
        streaming: normalizedStreamingPolicy(streaming),
        provider_type: providerType,
        upstream_model: upstreamModel.trim(),
        credential_ref: credentialRef.trim(),
        base_url: baseURL.trim(),
        region: region.trim(),
        max_parallel_requests: parsedMaxParallel,
        supports_chat: supportsChat,
        supports_responses: supportsResponses,
        supports_stream_chat: supportsStreamChat,
        supports_embeddings: supportsEmbeddings,
      })
      onSaved()
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : 'failed to update deployment')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={`Edit deployment · ${deployment.name}`} onClose={onClose} dismissible={false}>
      <form onSubmit={submit} className="space-y-4">
        {deployment.managed_by === 'config' && (
          <div className="scope-notice" role="note">
            <div>
              <strong>Defined in the config file</strong>
              <p>Changes made here are replaced the next time the gateway starts. Edit the config file to keep them.</p>
            </div>
          </div>
        )}
        <Field
          label="Name"
          hint="Name is pinned because aliases reference it. Recreate to rename."
        >
          <Input
            type="text" disabled value={deployment.name}
            className="cursor-not-allowed text-fg-subtle"
          />
        </Field>

        <Field label="Provider type">
          <Select
            value={providerType}
            onChange={(e) => setProviderType(e.target.value)}
          >
            <option value="openai">openai</option>
            <option value="azure_openai">azure_openai</option>
            <option value="anthropic">anthropic</option>
            <option value="mistral">mistral</option>
            <option value="groq">groq</option>
            <option value="together">together</option>
            <option value="fireworks">fireworks</option>
            <option value="openrouter">openrouter</option>
            <option value="cohere">cohere</option>
            <option value="openai_compatible">openai_compatible</option>
            <option value="ollama">ollama</option>
            <option value="vllm">vllm</option>
          </Select>
        </Field>

        <Field label="Upstream model">
          <Input
            type="text" required value={upstreamModel}
            onChange={(e) => setUpstreamModel(e.target.value)}
          />
        </Field>

        <Field
          label={credentialRequired ? 'Credential env var' : 'Credential env var (optional)'}
          required={credentialRequired}
          hint="Environment variable name only — never paste the secret here."
        >
          <Input
            type="text" required={credentialRequired} value={credentialRef}
            onChange={(e) => setCredentialRef(e.target.value)}
            className="font-mono"
            placeholder="OPENAI_API_KEY"
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Base URL (optional)">
            <Input
              type="text" value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
              className="font-mono"
              placeholder="https://api.openai.com/v1"
            />
          </Field>
          <Field label="Region (optional)">
            <Input
              type="text" value={region}
              onChange={(e) => setRegion(e.target.value)}
              className="font-mono"
              placeholder="us-east-1"
            />
          </Field>
        </div>

        <Field
          label="Max parallel requests (optional)"
          hint="Per gateway process. Leave blank for unlimited local concurrency."
        >
          <Input
            type="number" min="1" step="1" value={maxParallelRequests}
            onChange={(e) => setMaxParallelRequests(e.target.value)}
            className="font-mono"
            placeholder="Unlimited"
          />
        </Field>

        <div>
          <div className="text-xs font-medium text-fg-base">Capabilities</div>
          <div className="mt-2 space-y-1.5 rounded-md border border-border-base bg-bg-base p-3">
            <Capability
              label="Chat completions"
              hint="POST /v1/chat/completions"
              checked={supportsChat}
              onChange={setSupportsChat}
            />
            <Capability
              label="Streaming chat"
              hint="POST /v1/chat/completions with stream=true"
              checked={supportsStreamChat}
              onChange={setSupportsStreamChat}
              disabled={!supportsChat}
            />
            <Capability
              label="Native Responses"
              hint="POST /v1/responses — upstream must implement the native protocol"
              checked={supportsResponses}
              onChange={setSupportsResponses}
            />
            <Capability
              label="Embeddings"
              hint="POST /v1/embeddings"
              checked={supportsEmbeddings}
              onChange={setSupportsEmbeddings}
            />
          </div>
        </div>

        <StreamingPolicyFields value={streaming} onChange={setStreaming} />
        {err && <p className="text-xs text-danger">{err}</p>}
        <ModalFooter>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={busy}>
            {busy ? 'Saving…' : 'Save changes'}
          </Button>
        </ModalFooter>
      </form>
    </Modal>
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
        <div className="text-[11px] text-fg-subtle">{hint}</div>
      </div>
    </label>
  )
}
