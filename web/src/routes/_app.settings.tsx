import { createFileRoute } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { Check, Loader2, AlertCircle, X, Eye, EyeOff, Folder, ChevronRight, Home } from 'lucide-react'
import { cn } from '../lib/utils'

interface SettingsView {
  claude: {
    api_key_set: boolean
    api_key_hint?: string
    base_url?: string
    chat_model: string
    eval_model: string
    operator_model: string
    chat_model_default: string
    eval_model_default: string
    operator_model_default: string
  }
  tokens: { gh_set: boolean; gh_hint?: string; gitlab_set: boolean; gitlab_hint?: string }
  global: {
    poll_interval: number
    confidence_threshold: number
    agent_timeout: number
    max_concurrent_agents: number
    run_interval_minutes: number
    github_clone_dir?: string
    gitlab_clone_dir?: string
  }
}

export const Route = createFileRoute('/_app/settings')({
  component: SettingsPage,
})

// revealSecret asks the backend for the raw saved value of a single
// credential so the eye-icon toggle can show what's currently
// configured rather than just the typed-but-unsaved input. Returns ''
// if the secret isn't set or the request fails — callers should fall
// back to whatever placeholder they already had.
async function revealSecret(which: 'claude_api_key' | 'gh_token' | 'gitlab_token'): Promise<string> {
  try {
    const r = await fetch('/api/settings/reveal', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ which }),
    })
    if (!r.ok) return ''
    const d = await r.json()
    return typeof d.value === 'string' ? d.value : ''
  } catch {
    return ''
  }
}

function SettingsPage() {
  const [data, setData] = useState<SettingsView | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(() => {
    fetch('/api/settings', { cache: 'no-store' })
      .then(r => r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`)))
      .then(d => setData(d))
      .catch(e => setError(String(e.message || e)))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { refresh() }, [refresh])

  return (
    <div className="max-w-3xl mx-auto px-4 py-6 space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-foreground">Settings</h1>
        <p className="text-xs text-muted-foreground mt-1">
          Edits land in <code className="px-1 py-0.5 bg-secondary rounded text-[10px]">~/.clawflow/config/credentials.yaml</code> and{' '}
          <code className="px-1 py-0.5 bg-secondary rounded text-[10px]">config.yaml</code>. Sensitive
          fields are never shown in full — type the new value to change them, leave blank to keep.
        </p>
      </div>

      {loading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {error && (
        <div className="bg-red-50 border border-red-200 text-red-700 text-sm px-3 py-2 rounded">
          Failed to load settings: {error}
        </div>
      )}

      {data && (
        <>
          <ClaudeSection view={data.claude} onSaved={refresh} />
          <TokensSection view={data.tokens} onSaved={refresh} />
          <GlobalSection view={data.global} onSaved={refresh} />
        </>
      )}
    </div>
  )
}

// -----------------------------------------------------------------------------
// Claude API
// -----------------------------------------------------------------------------

function ClaudeSection({
  view, onSaved,
}: { view: SettingsView['claude']; onSaved: () => void }) {
  const [apiKey, setApiKey] = useState('')
  const [showKey, setShowKey] = useState(false)
  const [baseURL, setBaseURL] = useState(view.base_url ?? '')
  const [chatModel, setChatModel] = useState(view.chat_model ?? '')
  const [evalModel, setEvalModel] = useState(view.eval_model ?? '')
  const [operatorModel, setOperatorModel] = useState(view.operator_model ?? '')
  const [busy, setBusy] = useState(false)
  const [saveMsg, setSaveMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [testResult, setTestResult] = useState<{ ok: boolean; text: string } | null>(null)
  const [testing, setTesting] = useState(false)

  // Keep server-backed fields in sync when the parent reloads.
  useEffect(() => { setBaseURL(view.base_url ?? '') }, [view.base_url])
  useEffect(() => { setChatModel(view.chat_model ?? '') }, [view.chat_model])
  useEffect(() => { setEvalModel(view.eval_model ?? '') }, [view.eval_model])
  useEffect(() => { setOperatorModel(view.operator_model ?? '') }, [view.operator_model])

  const dirty =
    apiKey !== '' ||
    baseURL !== (view.base_url ?? '') ||
    chatModel !== (view.chat_model ?? '') ||
    evalModel !== (view.eval_model ?? '') ||
    operatorModel !== (view.operator_model ?? '')

  const save = () => {
    setBusy(true); setSaveMsg(null)
    const body: Record<string, string> = {}
    if (apiKey !== '') body.api_key = apiKey
    if (baseURL !== (view.base_url ?? '')) body.base_url = baseURL
    if (chatModel !== (view.chat_model ?? '')) body.chat_model = chatModel
    if (evalModel !== (view.eval_model ?? '')) body.eval_model = evalModel
    if (operatorModel !== (view.operator_model ?? '')) body.operator_model = operatorModel
    fetch('/api/settings/claude', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
      .then(async r => {
        const d = await r.json().catch(() => null)
        if (!r.ok) throw new Error((d && d.error) || `HTTP ${r.status}`)
      })
      .then(() => {
        setSaveMsg({ ok: true, text: 'Saved' })
        setApiKey('') // clear so the field shows as blank again
        onSaved()
      })
      .catch(e => setSaveMsg({ ok: false, text: String(e.message || e) }))
      .finally(() => setBusy(false))
  }

  const testConnection = () => {
    setTesting(true); setTestResult(null)
    // Connectivity test deliberately ignores the configured models —
    // it always pings haiku so a misconfigured operator/eval model
    // can't make the credentials look broken.
    fetch('/api/settings/claude/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        // Test with the IN-FLIGHT (unsaved) values if user typed any,
        // else fall back to whatever is currently saved.
        ...(apiKey !== '' ? { api_key: apiKey } : {}),
        ...(baseURL !== (view.base_url ?? '') ? { base_url: baseURL } : {}),
      }),
    })
      .then(r => r.json())
      .then(d => {
        if (d.status === 'ok') {
          setTestResult({ ok: true, text: `OK${d.reply ? ' — ' + truncate(d.reply, 80) : ''}` })
        } else {
          // Backend now returns a humanized `error` like
          // "<stderr/stdout> (exit status N)". Surface the full
          // text so the user can actually see what went wrong
          // instead of a bare "exit status 1".
          setTestResult({ ok: false, text: d.error || d.stderr || d.stdout || 'failed' })
        }
      })
      .catch(e => setTestResult({ ok: false, text: String(e.message || e) }))
      .finally(() => setTesting(false))
  }

  return (
    <Card title="Claude API" hint="Override ANTHROPIC_API_KEY / ANTHROPIC_BASE_URL / --model for spawned claude subprocesses. Leave blank to inherit OAuth/keychain and the claude CLI's default model.">
      <Row label="API key">
        <div className="flex-1 flex gap-2 items-center">
          <input
            type={showKey ? 'text' : 'password'}
            value={apiKey}
            onChange={e => setApiKey(e.target.value)}
            placeholder={view.api_key_set ? `configured · ${view.api_key_hint ?? ''}` : 'sk-ant-…'}
            className="flex-1 text-sm font-mono px-2 py-1 border border-border rounded bg-background"
          />
          <button
            type="button"
            onClick={async () => {
              // Switching from hidden→visible while the input is empty
              // means "show me what's actually saved" — fetch the raw
              // value into the field so the user can see it (and edit
              // it from there if needed). Once they type something the
              // input owns the value and we just toggle visibility.
              if (!showKey && apiKey === '' && view.api_key_set) {
                const v = await revealSecret('claude_api_key')
                if (v) setApiKey(v)
              }
              setShowKey(s => !s)
            }}
            className="text-muted-foreground hover:text-foreground"
            title={showKey ? 'Hide' : 'Show saved value'}
          >
            {showKey ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
          </button>
        </div>
      </Row>
      <Row label="Base URL">
        <input
          type="text"
          value={baseURL}
          onChange={e => setBaseURL(e.target.value)}
          placeholder="https://api.anthropic.com (default)"
          className="flex-1 text-sm font-mono px-2 py-1 border border-border rounded bg-background"
        />
      </Row>
      <ModelRow
        label="Chat model"
        hint="Used by the dashboard's chat drawer (clawflow chat)."
        value={chatModel}
        defaultValue={view.chat_model_default}
        onChange={setChatModel}
      />
      <ModelRow
        label="Eval model"
        hint="Used by evaluate-* operators (evaluate-bug, evaluate-feat)."
        value={evalModel}
        defaultValue={view.eval_model_default}
        onChange={setEvalModel}
      />
      <ModelRow
        label="Operator model"
        hint="Used by every other operator (implement, reply-comment, custom skills)."
        value={operatorModel}
        defaultValue={view.operator_model_default}
        onChange={setOperatorModel}
      />

      <div className="flex items-center gap-2 pt-2">
        <button
          type="button"
          onClick={save}
          disabled={!dirty || busy}
          className={cn(
            'text-sm px-3 py-1 rounded border',
            dirty && !busy
              ? 'bg-secondary hover:bg-secondary/70 border-border text-foreground'
              : 'bg-muted text-muted-foreground border-border cursor-not-allowed',
          )}
        >
          {busy ? <Loader2 className="w-3 h-3 animate-spin inline mr-1" /> : null}
          Save
        </button>
        <button
          type="button"
          onClick={testConnection}
          disabled={testing}
          className="text-sm px-3 py-1 rounded border border-border hover:bg-secondary/50 text-foreground"
        >
          {testing ? <Loader2 className="w-3 h-3 animate-spin inline mr-1" /> : null}
          Test connection
        </button>

        {saveMsg && <Status ok={saveMsg.ok} text={saveMsg.text} />}
        {testResult && testResult.ok && <Status ok text={testResult.text} />}
      </div>
      {/* Failure detail rendered as a full-width block so long
          claude error messages (auth failures, rate limits) are
          actually readable instead of being clipped to a badge. */}
      {testResult && !testResult.ok && (
        <pre className="mt-2 px-3 py-2 text-xs font-mono whitespace-pre-wrap break-words bg-red-50 border border-red-200 text-red-800 rounded max-h-48 overflow-auto">
          {testResult.text}
        </pre>
      )}
    </Card>
  )
}

// -----------------------------------------------------------------------------
// VCS Tokens
// -----------------------------------------------------------------------------

function TokensSection({
  view, onSaved,
}: { view: SettingsView['tokens']; onSaved: () => void }) {
  const [gh, setGh] = useState('')
  const [gitlab, setGitlab] = useState('')
  const [showGh, setShowGh] = useState(false)
  const [showGitlab, setShowGitlab] = useState(false)
  const [busy, setBusy] = useState(false)
  const [saveMsg, setSaveMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [testingGh, setTestingGh] = useState(false)
  const [testingGitlab, setTestingGitlab] = useState(false)
  const [testResultGh, setTestResultGh] = useState<{ ok: boolean; text: string } | null>(null)
  const [testResultGitlab, setTestResultGitlab] = useState<{ ok: boolean; text: string } | null>(null)

  const dirty = gh !== '' || gitlab !== ''

  const save = () => {
    setBusy(true); setSaveMsg(null)
    const body: Record<string, string> = {}
    if (gh !== '') body.gh_token = gh
    if (gitlab !== '') body.gitlab_token = gitlab
    fetch('/api/settings/tokens', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
      .then(async r => {
        const d = await r.json().catch(() => null)
        if (!r.ok) throw new Error((d && d.error) || `HTTP ${r.status}`)
      })
      .then(() => {
        setSaveMsg({ ok: true, text: 'Saved' })
        setGh(''); setGitlab('')
        onSaved()
      })
      .catch(e => setSaveMsg({ ok: false, text: String(e.message || e) }))
      .finally(() => setBusy(false))
  }

  const testGitHub = () => {
    setTestingGh(true); setTestResultGh(null)
    fetch('/api/settings/verify-token', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ platform: 'github' }),
    })
      .then(r => r.json())
      .then(d => {
        if (d.valid) {
          setTestResultGh({ ok: true, text: d.message || 'Connected' })
        } else {
          setTestResultGh({ ok: false, text: d.error || d.message || 'Connection failed' })
        }
      })
      .catch(e => setTestResultGh({ ok: false, text: String(e.message || e) }))
      .finally(() => setTestingGh(false))
  }

  const testGitLab = () => {
    setTestingGitlab(true); setTestResultGitlab(null)
    fetch('/api/settings/verify-token', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ platform: 'gitlab' }),
    })
      .then(r => r.json())
      .then(d => {
        if (d.valid) {
          setTestResultGitlab({ ok: true, text: d.message || 'Connected' })
        } else {
          setTestResultGitlab({ ok: false, text: d.error || d.message || 'Connection failed' })
        }
      })
      .catch(e => setTestResultGitlab({ ok: false, text: String(e.message || e) }))
      .finally(() => setTestingGitlab(false))
  }

  return (
    <Card title="VCS Tokens" hint="GitHub / GitLab personal access tokens. Used for issue + PR API calls.">
      <Row label="GitHub">
        <div className="flex-1 flex flex-col gap-2">
          <PasswordInput
            value={gh}
            onChange={setGh}
            show={showGh}
            onToggleShow={() => setShowGh(s => !s)}
            placeholder={view.gh_set ? `configured · ${view.gh_hint ?? ''}` : 'ghp_…'}
            onReveal={view.gh_set ? () => revealSecret('gh_token') : undefined}
          />
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={testGitHub}
              disabled={testingGh || !view.gh_set}
              className={cn(
                'text-xs px-2 py-1 rounded border',
                view.gh_set && !testingGh
                  ? 'border-border hover:bg-secondary/50 text-foreground'
                  : 'bg-muted text-muted-foreground border-border cursor-not-allowed',
              )}
            >
              {testingGh ? <Loader2 className="w-3 h-3 animate-spin inline mr-1" /> : null}
              Test Connection
            </button>
            {testResultGh && <Status ok={testResultGh.ok} text={testResultGh.text} />}
          </div>
        </div>
      </Row>
      <Row label="GitLab">
        <div className="flex-1 flex flex-col gap-2">
          <PasswordInput
            value={gitlab}
            onChange={setGitlab}
            show={showGitlab}
            onToggleShow={() => setShowGitlab(s => !s)}
            placeholder={view.gitlab_set ? `configured · ${view.gitlab_hint ?? ''}` : 'glpat-…'}
            onReveal={view.gitlab_set ? () => revealSecret('gitlab_token') : undefined}
          />
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={testGitLab}
              disabled={testingGitlab || !view.gitlab_set}
              className={cn(
                'text-xs px-2 py-1 rounded border',
                view.gitlab_set && !testingGitlab
                  ? 'border-border hover:bg-secondary/50 text-foreground'
                  : 'bg-muted text-muted-foreground border-border cursor-not-allowed',
              )}
            >
              {testingGitlab ? <Loader2 className="w-3 h-3 animate-spin inline mr-1" /> : null}
              Test Connection
            </button>
            {testResultGitlab && <Status ok={testResultGitlab.ok} text={testResultGitlab.text} />}
          </div>
        </div>
      </Row>

      <div className="flex items-center gap-2 pt-2">
        <button
          type="button"
          onClick={save}
          disabled={!dirty || busy}
          className={cn(
            'text-sm px-3 py-1 rounded border',
            dirty && !busy
              ? 'bg-secondary hover:bg-secondary/70 border-border text-foreground'
              : 'bg-muted text-muted-foreground border-border cursor-not-allowed',
          )}
        >
          {busy ? <Loader2 className="w-3 h-3 animate-spin inline mr-1" /> : null}
          Save
        </button>
        {saveMsg && <Status ok={saveMsg.ok} text={saveMsg.text} />}
      </div>
    </Card>
  )
}

// -----------------------------------------------------------------------------
// Global settings
// -----------------------------------------------------------------------------

function GlobalSection({
  view, onSaved,
}: { view: SettingsView['global']; onSaved: () => void }) {
  const [pollInterval, setPollInterval] = useState(view.poll_interval)
  const [confidence, setConfidence] = useState(view.confidence_threshold)
  const [timeout, setTimeoutVal] = useState(view.agent_timeout)
  const [maxConcurrent, setMaxConcurrent] = useState(view.max_concurrent_agents)
  const [runInterval, setRunInterval] = useState(view.run_interval_minutes)
  const [ghDir, setGhDir] = useState(view.github_clone_dir ?? '')
  const [glDir, setGlDir] = useState(view.gitlab_clone_dir ?? '')
  const [busy, setBusy] = useState(false)
  const [saveMsg, setSaveMsg] = useState<{ ok: boolean; text: string } | null>(null)

  // Resync if parent reloads.
  useEffect(() => {
    setPollInterval(view.poll_interval)
    setConfidence(view.confidence_threshold)
    setTimeoutVal(view.agent_timeout)
    setMaxConcurrent(view.max_concurrent_agents)
    setRunInterval(view.run_interval_minutes)
    setGhDir(view.github_clone_dir ?? '')
    setGlDir(view.gitlab_clone_dir ?? '')
  }, [view])

  const dirty =
    pollInterval !== view.poll_interval ||
    confidence !== view.confidence_threshold ||
    timeout !== view.agent_timeout ||
    maxConcurrent !== view.max_concurrent_agents ||
    runInterval !== view.run_interval_minutes ||
    ghDir !== (view.github_clone_dir ?? '') ||
    glDir !== (view.gitlab_clone_dir ?? '')

  const save = () => {
    setBusy(true); setSaveMsg(null)
    fetch('/api/settings/global', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        poll_interval: pollInterval,
        confidence_threshold: confidence,
        agent_timeout: timeout,
        max_concurrent_agents: maxConcurrent,
        run_interval_minutes: runInterval,
        github_clone_dir: ghDir,
        gitlab_clone_dir: glDir,
      }),
    })
      .then(async r => {
        const d = await r.json().catch(() => null)
        if (!r.ok) throw new Error((d && d.error) || `HTTP ${r.status}`)
      })
      .then(() => { setSaveMsg({ ok: true, text: 'Saved' }); onSaved() })
      .catch(e => setSaveMsg({ ok: false, text: String(e.message || e) }))
      .finally(() => setBusy(false))
  }

  return (
    <Card title="Global" hint="Polling cadence, agent limits, and where new clones land.">
      <Row label="Poll interval (s)">
        <NumInput value={pollInterval} onChange={setPollInterval} min={5} />
      </Row>
      <Row label="Agent timeout (s)">
        <NumInput value={timeout} onChange={setTimeoutVal} min={60} />
      </Row>
      <Row label="Confidence threshold">
        <NumInput value={confidence} onChange={setConfidence} min={0} max={100} />
      </Row>
      <Row label="Max concurrent agents">
        <NumInput value={maxConcurrent} onChange={setMaxConcurrent} min={1} max={32} />
      </Row>
      <Row label="Auto-run interval (min)" hint="0 disables periodic auto-run; only the manual Run button works.">
        <NumInput value={runInterval} onChange={setRunInterval} min={0} max={1440} />
      </Row>
      <Row label="GitHub clone dir">
        <DirectoryInput
          value={ghDir}
          onChange={setGhDir}
          placeholder="~/github (default)"
        />
      </Row>
      <Row label="GitLab clone dir">
        <DirectoryInput
          value={glDir}
          onChange={setGlDir}
          placeholder="~/gitlab (default)"
        />
      </Row>

      <div className="flex items-center gap-2 pt-2">
        <button
          type="button"
          onClick={save}
          disabled={!dirty || busy}
          className={cn(
            'text-sm px-3 py-1 rounded border',
            dirty && !busy
              ? 'bg-secondary hover:bg-secondary/70 border-border text-foreground'
              : 'bg-muted text-muted-foreground border-border cursor-not-allowed',
          )}
        >
          {busy ? <Loader2 className="w-3 h-3 animate-spin inline mr-1" /> : null}
          Save
        </button>
        {saveMsg && <Status ok={saveMsg.ok} text={saveMsg.text} />}
      </div>
    </Card>
  )
}

// -----------------------------------------------------------------------------
// Reusable bits
// -----------------------------------------------------------------------------

function Card({ title, hint, children }: { title: string; hint?: string; children: React.ReactNode }) {
  return (
    <section className="bg-card border border-border rounded-xl p-4 space-y-2">
      <div>
        <h2 className="text-sm font-semibold text-foreground">{title}</h2>
        {hint && <p className="text-xs text-muted-foreground mt-0.5">{hint}</p>}
      </div>
      <div className="space-y-2">{children}</div>
    </section>
  )
}

function Row({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-3">
      <label className="text-xs text-muted-foreground w-40 shrink-0 pt-1.5">{label}</label>
      <div className="flex-1 flex flex-col gap-1">
        <div className="flex items-center gap-2">{children}</div>
        {hint && <div className="text-[11px] text-muted-foreground/80">{hint}</div>}
      </div>
    </div>
  )
}

function NumInput({ value, onChange, min, max }: { value: number; onChange: (n: number) => void; min?: number; max?: number }) {
  return (
    <input
      type="number"
      value={value}
      min={min}
      max={max}
      onChange={e => {
        const n = parseInt(e.target.value, 10)
        if (!isNaN(n)) onChange(n)
      }}
      className="w-32 text-sm font-mono px-2 py-1 border border-border rounded bg-background"
    />
  )
}

function PasswordInput({
  value, onChange, show, onToggleShow, placeholder, onReveal,
}: {
  value: string
  onChange: (v: string) => void
  show: boolean
  onToggleShow: () => void
  placeholder?: string
  // onReveal, when provided, is invoked the moment the user toggles
  // visibility ON while the field is empty — the typical case of
  // "I want to see what's already saved". Returning a non-empty
  // string populates the input; returning '' means "no saved value
  // to reveal", in which case we just flip visibility.
  onReveal?: () => Promise<string>
}) {
  return (
    <div className="flex-1 flex gap-2 items-center">
      <input
        type={show ? 'text' : 'password'}
        value={value}
        onChange={e => onChange(e.target.value)}
        placeholder={placeholder}
        className="flex-1 text-sm font-mono px-2 py-1 border border-border rounded bg-background"
      />
      <button
        type="button"
        onClick={async () => {
          if (!show && value === '' && onReveal) {
            const v = await onReveal()
            if (v) onChange(v)
          }
          onToggleShow()
        }}
        className="text-muted-foreground hover:text-foreground"
        title={show ? 'Hide' : 'Show saved value'}
      >
        {show ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
      </button>
    </div>
  )
}

// MODEL_PRESETS is the curated dropdown list. Empty value (the
// default-marker option) means "inherit whatever DefaultChatModel /
// DefaultEvalModel / DefaultOperatorModel resolves to". Three groups:
//
//   - aliases: most portable (work on Anthropic direct, Kiro, cc-proxy)
//   - dash form: Anthropic standard, pinned versions
//   - dot form: Kiro proxy specific (matches its /v1/models verbatim,
//     including the 1M-context Sonnet 4.6 it ships)
//
// A user typing in their own value (e.g. claude-haiku-4-5-20251001 to
// pin to the dated release) still gets surfaced as a "(custom)" entry.
const MODEL_PRESETS = [
  // family aliases — recommended default
  'haiku',
  'sonnet',
  'opus',
  // Anthropic dashed IDs — pin a specific version
  'claude-haiku-4-5',
  'claude-sonnet-4-6',
  'claude-opus-4-7',
  // Kiro proxy dot IDs — match its /v1/models listing
  'claude-sonnet-4.6',
  'claude-opus-4.6',
] as const

function ModelRow({
  label, hint, value, defaultValue, onChange,
}: {
  label: string
  hint: string
  value: string
  defaultValue: string
  onChange: (v: string) => void
}) {
  // If the saved value isn't in MODEL_PRESETS, surface it as a custom
  // option at the top of the dropdown so the user can see what's
  // currently in effect without us silently dropping their pin.
  const inPresets = (MODEL_PRESETS as readonly string[]).includes(value)
  const showCustom = value !== '' && !inPresets

  return (
    <Row label={label}>
      <div className="flex-1 flex flex-col gap-0.5">
        <select
          value={value}
          onChange={e => onChange(e.target.value)}
          className="text-sm font-mono px-2 py-1 border border-border rounded bg-background"
        >
          <option value="">(default — {defaultValue})</option>
          {showCustom && <option value={value}>{value} (custom)</option>}
          {MODEL_PRESETS.map(m => (
            <option key={m} value={m}>{m}</option>
          ))}
        </select>
        <span className="text-[11px] text-muted-foreground">{hint}</span>
      </div>
    </Row>
  )
}

function Status({ ok, text }: { ok: boolean; text: string }) {
  return (
    <span className={cn(
      'inline-flex items-center gap-1 text-xs px-1.5 py-0.5 rounded border',
      ok
        ? 'bg-green-50 border-green-200 text-green-700'
        : 'bg-red-50 border-red-200 text-red-700',
    )}>
      {ok ? <Check className="w-3 h-3" /> : <AlertCircle className="w-3 h-3" />}
      {text}
    </span>
  )
}

function truncate(s: string, n: number): string {
  return s.length <= n ? s : s.slice(0, n) + '…'
}

// -----------------------------------------------------------------------------
// Directory Input with Browser
// -----------------------------------------------------------------------------

interface DirectoryBrowserData {
  current_path: string
  parent: string
  directories: Array<{ name: string; path: string }>
  error?: string
}

function DirectoryInput({
  value, onChange, placeholder,
}: {
  value: string
  onChange: (v: string) => void
  placeholder?: string
}) {
  const [showBrowser, setShowBrowser] = useState(false)

  return (
    <>
      <div className="flex-1 flex gap-2 items-center">
        <input
          type="text"
          value={value}
          onChange={e => onChange(e.target.value)}
          placeholder={placeholder}
          className="flex-1 text-sm font-mono px-2 py-1 border border-border rounded bg-background"
        />
        <button
          type="button"
          onClick={() => setShowBrowser(true)}
          className="text-muted-foreground hover:text-foreground"
          title="Browse directories"
        >
          <Folder className="w-4 h-4" />
        </button>
      </div>
      {showBrowser && (
        <DirectoryBrowser
          initialPath={value || undefined}
          onSelect={(path) => {
            onChange(path)
            setShowBrowser(false)
          }}
          onClose={() => setShowBrowser(false)}
        />
      )}
    </>
  )
}

function DirectoryBrowser({
  initialPath, onSelect, onClose,
}: {
  initialPath?: string
  onSelect: (path: string) => void
  onClose: () => void
}) {
  const [currentPath, setCurrentPath] = useState(initialPath || '')
  const [data, setData] = useState<DirectoryBrowserData | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const loadPath = useCallback((path: string) => {
    setLoading(true)
    setError(null)
    const url = `/api/browse-directory${path ? `?path=${encodeURIComponent(path)}` : ''}`
    fetch(url)
      .then(r => r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`)))
      .then((d: DirectoryBrowserData) => {
        setData(d)
        setCurrentPath(d.current_path)
        if (d.error) {
          setError(d.error)
        }
      })
      .catch(e => setError(String(e.message || e)))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => {
    loadPath(initialPath || '')
  }, [initialPath, loadPath])

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50" onClick={onClose}>
      <div
        className="bg-card border border-border rounded-lg shadow-lg w-full max-w-2xl max-h-[80vh] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <h3 className="text-sm font-semibold text-foreground">Select Directory</h3>
          <button
            type="button"
            onClick={onClose}
            className="text-muted-foreground hover:text-foreground"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Current path bar */}
        <div className="px-4 py-2 border-b border-border bg-secondary/30">
          <div className="flex items-center gap-2 text-xs font-mono text-foreground">
            <button
              type="button"
              onClick={() => loadPath('')}
              className="text-muted-foreground hover:text-foreground"
              title="Go to home directory"
            >
              <Home className="w-3.5 h-3.5" />
            </button>
            <span className="flex-1 truncate">{currentPath || '(loading...)'}</span>
          </div>
        </div>

        {/* Directory list */}
        <div className="flex-1 overflow-y-auto p-2">
          {loading && (
            <div className="flex items-center justify-center py-8 text-muted-foreground">
              <Loader2 className="w-4 h-4 animate-spin mr-2" />
              Loading...
            </div>
          )}
          {error && (
            <div className="px-3 py-2 text-xs text-red-700 bg-red-50 border border-red-200 rounded">
              {error}
            </div>
          )}
          {data && !loading && (
            <div className="space-y-0.5">
              {/* Parent directory */}
              {data.parent && (
                <button
                  type="button"
                  onClick={() => loadPath(data.parent)}
                  className="w-full flex items-center gap-2 px-3 py-2 text-sm text-muted-foreground hover:bg-secondary/50 rounded"
                >
                  <Folder className="w-4 h-4 shrink-0" />
                  <span className="flex-1 text-left">..</span>
                </button>
              )}
              {/* Subdirectories */}
              {data.directories.map(dir => (
                <button
                  key={dir.path}
                  type="button"
                  onClick={() => loadPath(dir.path)}
                  className="w-full flex items-center gap-2 px-3 py-2 text-sm text-foreground hover:bg-secondary/50 rounded group"
                >
                  <Folder className="w-4 h-4 shrink-0 text-muted-foreground" />
                  <span className="flex-1 text-left">{dir.name}</span>
                  <ChevronRight className="w-3.5 h-3.5 text-muted-foreground opacity-0 group-hover:opacity-100" />
                </button>
              ))}
              {data.directories.length === 0 && !data.parent && !data.error && (
                <div className="px-3 py-8 text-center text-xs text-muted-foreground">
                  No subdirectories
                </div>
              )}
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between px-4 py-3 border-t border-border">
          <span className="text-xs text-muted-foreground">
            {data && !data.error ? `${data.directories.length} subdirectories` : ''}
          </span>
          <div className="flex gap-2">
            <button
              type="button"
              onClick={onClose}
              className="text-sm px-3 py-1 rounded border border-border hover:bg-secondary/50 text-foreground"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => onSelect(currentPath)}
              disabled={!currentPath}
              className={cn(
                'text-sm px-3 py-1 rounded border',
                currentPath
                  ? 'bg-secondary hover:bg-secondary/70 border-border text-foreground'
                  : 'bg-muted text-muted-foreground border-border cursor-not-allowed',
              )}
            >
              Select
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

// Suppress unused-import warning when build-flag-driven dead-code
// elimination drops the X icon in production. Keep it referenced.
export const _icons = { X }
