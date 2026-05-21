import { createFileRoute } from '@tanstack/react-router'
import { useCallback, useEffect, useState } from 'react'
import { Check, Loader2, AlertCircle, X, Eye, EyeOff, Folder, ChevronRight, Home, Copy, ExternalLink, RefreshCw, Upload, Download, LogOut } from 'lucide-react'
import { cn } from '../lib/utils'
import { emitConfigChanged, useConfigChanged } from '../lib/configEvents'
import { ProvidersSection } from '../components/ProvidersSection'

interface SettingsView {
  tokens: { gh_set: boolean; gh_hint?: string; gitlab_set: boolean; gitlab_hint?: string }
  global: {
    poll_interval: number
    confidence_threshold: number
    agent_timeout: number
    max_concurrent_agents: number
    run_interval_minutes: number
    github_clone_dir?: string
    gitlab_clone_dir?: string
    gitlab_url?: string
    terminal?: string
    default_ide?: string
    require_binding?: boolean
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
  useConfigChanged(refresh)

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
          <ProvidersSection />
          <TokensSection view={data.tokens} onSaved={refresh} />
          <GlobalSection view={data.global} onSaved={refresh} />
          <SyncSection />
        </>
      )}
    </div>
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
  const [gitlabURL, setGitlabURL] = useState(view.gitlab_url ?? '')
  const [terminal, setTerminal] = useState(view.terminal ?? '')
  const [defaultIDE, setDefaultIDE] = useState(view.default_ide ?? '')
  const [requireBinding, setRequireBinding] = useState(view.require_binding ?? false)
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
    setGitlabURL(view.gitlab_url ?? '')
    setTerminal(view.terminal ?? '')
    setDefaultIDE(view.default_ide ?? '')
    setRequireBinding(view.require_binding ?? false)
  }, [view])

  const dirty =
    pollInterval !== view.poll_interval ||
    confidence !== view.confidence_threshold ||
    timeout !== view.agent_timeout ||
    maxConcurrent !== view.max_concurrent_agents ||
    runInterval !== view.run_interval_minutes ||
    ghDir !== (view.github_clone_dir ?? '') ||
    glDir !== (view.gitlab_clone_dir ?? '') ||
    gitlabURL !== (view.gitlab_url ?? '') ||
    terminal !== (view.terminal ?? '') ||
    defaultIDE !== (view.default_ide ?? '') ||
    requireBinding !== (view.require_binding ?? false)

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
        gitlab_url: gitlabURL,
        terminal: terminal,
        default_ide: defaultIDE,
        require_binding: requireBinding,
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
      <Row label="GitLab Instance URL" hint="Full URL including protocol (http:// or https://) and port if needed">
        <input
          type="text"
          value={gitlabURL}
          onChange={e => setGitlabURL(e.target.value)}
          placeholder="https://gitlab.com or http://git.internal.com:8080"
          className="flex-1 text-sm font-mono px-2 py-1 border border-border rounded bg-background"
        />
      </Row>
      <Row label="Chat terminal" hint="Terminal used for chat sessions. VS Code supports image paste.">
        <select
          value={terminal}
          onChange={e => setTerminal(e.target.value)}
          className="flex-1 text-sm px-2 py-1 border border-border rounded bg-background"
        >
          <option value="">System default (Terminal.app / xterm)</option>
          <option value="vscode">VS Code (supports image paste)</option>
          <option value="iterm">iTerm2 (macOS)</option>
        </select>
      </Row>
      <Row label="Default IDE" hint="IDE used by the 'Open in IDE' button on the repos list.">
        <select
          value={defaultIDE}
          onChange={e => setDefaultIDE(e.target.value)}
          className="flex-1 text-sm px-2 py-1 border border-border rounded bg-background"
        >
          <option value="">VS Code (default)</option>
          <option value="vscode">VS Code</option>
          <option value="cursor">Cursor</option>
          <option value="qoder">Qoder</option>
          <option value="vscode-insiders">VS Code Insiders</option>
        </select>
      </Row>
      <Row label="Require binding" hint="When enabled, repos with no bound machine are skipped during runs.">
        <input
          type="checkbox"
          checked={requireBinding}
          onChange={e => setRequireBinding(e.target.checked)}
          className="rounded border-border w-4 h-4"
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
// Sync
// -----------------------------------------------------------------------------

interface SyncStatus {
  gist_id: string
  gh_token_set: boolean
  last_synced_at?: string
}

function SyncSection() {
  const [status, setStatus] = useState<SyncStatus | null>(null)
  const [loadErr, setLoadErr] = useState<string | null>(null)

  // Login form state
  const [token, setToken] = useState('')
  const [showToken, setShowToken] = useState(false)
  const [loginBusy, setLoginBusy] = useState(false)
  const [loginMsg, setLoginMsg] = useState<{ ok: boolean; text: string } | null>(null)

  // Login result — GitHub username returned by POST /api/login
  const [ghLogin, setGhLogin] = useState<string | null>(null)

  // Action state
  const [pushBusy, setPushBusy] = useState(false)
  const [pushMsg, setPushMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [pullBusy, setPullBusy] = useState(false)
  const [pullMsg, setPullMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [disconnectBusy, setDisconnectBusy] = useState(false)
  const [disconnectMsg, setDisconnectMsg] = useState<{ ok: boolean; text: string } | null>(null)

  // Clipboard copy state for Gist ID
  const [copied, setCopied] = useState(false)

  const loadStatus = useCallback(() => {
    setLoadErr(null)
    fetch('/api/sync/status', { cache: 'no-store' })
      .then(r => r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`)))
      .then((d: SyncStatus) => setStatus(d))
      .catch(e => setLoadErr(String(e.message || e)))
  }, [])

  useEffect(() => { loadStatus() }, [loadStatus])

  const handleLogin = () => {
    setLoginBusy(true); setLoginMsg(null)
    fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token }),
    })
      .then(r => r.json())
      .then(d => {
        if (d.status === 'ok') {
          setLoginMsg({ ok: true, text: `Connected as ${d.login}` })
          setGhLogin(d.login)
          setToken('')
          loadStatus()
          emitConfigChanged()
        } else {
          setLoginMsg({ ok: false, text: d.error || 'Login failed' })
        }
      })
      .catch(e => setLoginMsg({ ok: false, text: String(e.message || e) }))
      .finally(() => setLoginBusy(false))
  }

  const handlePush = () => {
    setPushBusy(true); setPushMsg(null)
    fetch('/api/sync/push', { method: 'POST' })
      .then(r => r.json())
      .then(d => {
        if (d.status === 'ok') {
          setPushMsg({ ok: true, text: 'Config pushed' + (d.gist_id ? ` · ${d.gist_id.slice(0, 8)}…` : '') })
          loadStatus()
        } else {
          setPushMsg({ ok: false, text: d.error || 'Push failed' })
        }
      })
      .catch(e => setPushMsg({ ok: false, text: String(e.message || e) }))
      .finally(() => setPushBusy(false))
  }

  const handlePull = () => {
    setPullBusy(true); setPullMsg(null)
    fetch('/api/sync/pull', { method: 'POST' })
      .then(r => r.json())
      .then(d => {
        if (d.status === 'ok') {
          setPullMsg({ ok: true, text: 'Config pulled and merged' })
          loadStatus()
          emitConfigChanged()
        } else {
          setPullMsg({ ok: false, text: d.error || 'Pull failed' })
        }
      })
      .catch(e => setPullMsg({ ok: false, text: String(e.message || e) }))
      .finally(() => setPullBusy(false))
  }

  const handleDisconnect = () => {
    if (!window.confirm('Disconnect GitHub sync?\n\nThis clears the stored token and Gist ID. Your local config is not affected.')) return
    setDisconnectBusy(true); setDisconnectMsg(null)
    fetch('/api/settings/tokens', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ gh_token: '' }),
    })
      .then(async r => {
        const d = await r.json().catch(() => null)
        if (!r.ok) throw new Error((d && d.error) || `HTTP ${r.status}`)
      })
      .then(() => {
        setDisconnectMsg({ ok: true, text: 'Disconnected' })
        setGhLogin(null)
        loadStatus()
      })
      .catch(e => setDisconnectMsg({ ok: false, text: String(e.message || e) }))
      .finally(() => setDisconnectBusy(false))
  }

  const copyGistID = (id: string) => {
    navigator.clipboard.writeText(id).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }).catch(() => {})
  }

  const connected = status?.gh_token_set ?? false
  const gistID = status?.gist_id ?? ''

  return (
    <Card
      title="Sync"
      hint="Sync repos, settings, and project knowledge files across machines via a private GitHub Gist. Credentials and local_path stay local."
    >
      {loadErr && (
        <div className="text-xs text-red-700 bg-red-50 border border-red-200 px-2 py-1.5 rounded">
          Failed to load sync status: {loadErr}
        </div>
      )}

      {/* Connection status */}
      {status && (
        <Row label="Status">
          {connected ? (
            <div className="flex-1 flex flex-col gap-1">
              <div className="flex items-center gap-2 text-sm">
                <span className="inline-flex items-center gap-1 text-xs px-1.5 py-0.5 rounded border bg-green-50 border-green-200 text-green-700">
                  <Check className="w-3 h-3" />
                  Connected{ghLogin ? ` as ${ghLogin}` : ''}
                </span>
              </div>
              {gistID && (
                <div className="flex items-center gap-1.5 mt-0.5">
                  <span className="text-xs text-muted-foreground">Gist ID:</span>
                  <code className="text-xs font-mono text-foreground bg-secondary px-1.5 py-0.5 rounded select-all">
                    {gistID}
                  </code>
                  <button
                    type="button"
                    onClick={() => copyGistID(gistID)}
                    className="text-muted-foreground hover:text-foreground transition-colors"
                    title="Copy Gist ID"
                    aria-label="Copy Gist ID"
                  >
                    {copied ? <Check className="w-3.5 h-3.5 text-green-600" /> : <Copy className="w-3.5 h-3.5" />}
                  </button>
                  <a
                    href={`https://gist.github.com/${gistID}`}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-muted-foreground hover:text-foreground transition-colors"
                    title="Open Gist on GitHub"
                    aria-label="Open Gist on GitHub"
                  >
                    <ExternalLink className="w-3.5 h-3.5" />
                  </a>
                </div>
              )}
              {status.last_synced_at && (
                <div className="text-[11px] text-muted-foreground">
                  Last synced: {new Date(status.last_synced_at).toLocaleString()}
                </div>
              )}
            </div>
          ) : (
            <span className="text-xs text-muted-foreground">Not connected</span>
          )}
        </Row>
      )}

      {/* Connect form — shown when not connected */}
      {status && !connected && (
        <Row label="GitHub token" hint={
          <>
            Needs the <code className="text-[10px] bg-secondary px-0.5 rounded">gist</code> scope.{' '}
            <a
              href="https://github.com/settings/tokens/new?scopes=gist&description=clawflow-sync"
              target="_blank"
              rel="noopener noreferrer"
              className="underline underline-offset-2 hover:text-foreground"
            >
              Create token on GitHub
            </a>
          </>
        }>
          <div className="flex-1 flex gap-2 items-center">
            <input
              type={showToken ? 'text' : 'password'}
              value={token}
              onChange={e => setToken(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter' && token) handleLogin() }}
              placeholder="ghp_…"
              className="flex-1 text-sm font-mono px-2 py-1 border border-border rounded bg-background"
              aria-label="GitHub personal access token"
            />
            <button
              type="button"
              onClick={() => setShowToken(s => !s)}
              className="text-muted-foreground hover:text-foreground"
              title={showToken ? 'Hide' : 'Show'}
              aria-label={showToken ? 'Hide token' : 'Show token'}
            >
              {showToken ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
            </button>
            <button
              type="button"
              onClick={handleLogin}
              disabled={!token || loginBusy}
              className={cn(
                'text-sm px-3 py-1 rounded border',
                token && !loginBusy
                  ? 'bg-secondary hover:bg-secondary/70 border-border text-foreground'
                  : 'bg-muted text-muted-foreground border-border cursor-not-allowed',
              )}
            >
              {loginBusy ? <Loader2 className="w-3 h-3 animate-spin inline mr-1" /> : null}
              Connect
            </button>
          </div>
          {loginMsg && (
            <div className="mt-1">
              <Status ok={loginMsg.ok} text={loginMsg.text} />
            </div>
          )}
        </Row>
      )}

      {/* Actions — shown when connected */}
      {status && connected && (
        <Row label="Actions">
          <div className="flex-1 flex flex-col gap-2">
            <div className="flex flex-wrap items-center gap-2">
              <button
                type="button"
                onClick={handlePush}
                disabled={pushBusy}
                className="text-sm px-3 py-1 rounded border border-border hover:bg-secondary/50 text-foreground inline-flex items-center gap-1.5"
              >
                {pushBusy
                  ? <Loader2 className="w-3 h-3 animate-spin" />
                  : <Upload className="w-3 h-3" />}
                Push config
              </button>
              <button
                type="button"
                onClick={handlePull}
                disabled={pullBusy}
                className="text-sm px-3 py-1 rounded border border-border hover:bg-secondary/50 text-foreground inline-flex items-center gap-1.5"
              >
                {pullBusy
                  ? <Loader2 className="w-3 h-3 animate-spin" />
                  : <Download className="w-3 h-3" />}
                Pull config
              </button>
              <button
                type="button"
                onClick={loadStatus}
                className="text-sm px-2 py-1 rounded border border-border hover:bg-secondary/50 text-muted-foreground"
                title="Refresh sync status"
                aria-label="Refresh sync status"
              >
                <RefreshCw className="w-3 h-3" />
              </button>
              <button
                type="button"
                onClick={handleDisconnect}
                disabled={disconnectBusy}
                className="text-sm px-3 py-1 rounded border border-red-200 text-red-600 hover:bg-red-50 transition-colors inline-flex items-center gap-1.5"
              >
                {disconnectBusy
                  ? <Loader2 className="w-3 h-3 animate-spin" />
                  : <LogOut className="w-3 h-3" />}
                Disconnect
              </button>
            </div>
            <div className="flex flex-wrap gap-2">
              {pushMsg && <Status ok={pushMsg.ok} text={pushMsg.text} />}
              {pullMsg && <Status ok={pullMsg.ok} text={pullMsg.text} />}
              {disconnectMsg && <Status ok={disconnectMsg.ok} text={disconnectMsg.text} />}
            </div>
          </div>
        </Row>
      )}

      {/* Info text */}
      <div className="text-[11px] text-muted-foreground/80 pt-2 space-y-1.5 border-t border-border mt-1">
        <p>
          <span className="font-medium text-muted-foreground">Synced:</span>{' '}
          <code className="text-[10px] bg-secondary px-1 rounded">config.yaml</code> (repos, settings, operator config) plus project knowledge files
          (<code className="text-[10px] bg-secondary px-1 rounded">.md</code>, <code className="text-[10px] bg-secondary px-1 rounded">.yaml</code>)
          under <code className="text-[10px] bg-secondary px-1 rounded">~/.clawflow/projects/&lt;name&gt;/</code> — e.g. CLAUDE.md, context.md, testing.md, project.yaml.
        </p>
        <p>
          <span className="font-medium text-muted-foreground">Never synced:</span>{' '}
          credentials (tokens, API keys), <code className="text-[10px] bg-secondary px-1 rounded">local_path</code> overrides,
          and runtime caches (<code className="text-[10px] bg-secondary px-1 rounded">generate-*.json</code>).
        </p>
        <details className="pt-1">
          <summary className="cursor-pointer text-muted-foreground hover:text-foreground select-none">How it works</summary>
          <div className="pt-1.5 space-y-1 pl-2">
            <p>
              Gist is a flat namespace, so paths are encoded with <code className="text-[10px] bg-secondary px-1 rounded">--</code> as separator:
              <code className="text-[10px] bg-secondary px-1 rounded ml-1">projects/clawflow/context.md</code>
              {' '}↔{' '}
              <code className="text-[10px] bg-secondary px-1 rounded">projects--clawflow--context.md</code>.
            </p>
            <p>
              Pull is cloud-wins for individual files; <code className="text-[10px] bg-secondary px-1 rounded">repos</code> entries are union-merged
              so each machine keeps its own <code className="text-[10px] bg-secondary px-1 rounded">local_path</code>.
            </p>
            <p>
              Auto-push runs after <code className="text-[10px] bg-secondary px-1 rounded">clawflow run</code>; auto-pull runs at
              <code className="text-[10px] bg-secondary px-1 rounded ml-1">clawflow web</code> /
              <code className="text-[10px] bg-secondary px-1 rounded ml-1">run</code> boot.
            </p>
          </div>
        </details>
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

function Row({ label, hint, children }: { label: string; hint?: React.ReactNode; children: React.ReactNode }) {
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
// Exported so ProvidersSection can reuse the same list.
export const MODEL_PRESETS = [
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
            <span className="flex-1 truncate" title={currentPath || '(loading...)'}>{currentPath || '(loading...)'}</span>
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
