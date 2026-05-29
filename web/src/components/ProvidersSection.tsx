import { useCallback, useEffect, useRef, useState } from 'react'
import {
  DndContext,
  closestCenter,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  DragEndEvent,
} from '@dnd-kit/core'
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import {
  GripVertical,
  Plus,
  Trash2,
  Pencil,
  Eye,
  EyeOff,
  Loader2,
  Check,
  AlertCircle,
  X,
  FlaskConical,
  ChevronDown,
} from 'lucide-react'
import { cn } from '../lib/utils'
import { MODEL_PRESETS } from '../routes/_app.settings'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ProviderView {
  name: string
  base_url?: string
  api_key_set: boolean
  api_key_hint?: string
  chat_model: string
  eval_model: string
  operator_model: string
  chat_model_default: string
  eval_model_default: string
  operator_model_default: string
  enabled: boolean
  index: number
}

// ---------------------------------------------------------------------------
// API helpers
// ---------------------------------------------------------------------------

async function fetchProviders(): Promise<ProviderView[]> {
  const r = await fetch('/api/providers', { cache: 'no-store' })
  if (!r.ok) throw new Error(`HTTP ${r.status}`)
  return r.json()
}

async function addProvider(body: {
  name: string
  base_url?: string
  api_key?: string
  chat_model?: string
  eval_model?: string
  operator_model?: string
  enabled: boolean
}): Promise<ProviderView> {
  const r = await fetch('/api/providers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const d = await r.json()
  if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`)
  return d
}

async function updateProvider(
  idx: number,
  body: { name?: string; base_url?: string; api_key?: string; chat_model?: string; eval_model?: string; operator_model?: string; enabled?: boolean },
): Promise<ProviderView> {
  const r = await fetch(`/api/providers/${idx}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const d = await r.json()
  if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`)
  return d
}

async function deleteProvider(idx: number): Promise<void> {
  const r = await fetch(`/api/providers/${idx}`, { method: 'DELETE' })
  if (!r.ok) {
    const d = await r.json().catch(() => null)
    throw new Error((d && d.error) || `HTTP ${r.status}`)
  }
}

async function reorderProviders(order: string[]): Promise<ProviderView[]> {
  const r = await fetch('/api/providers/reorder', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ order }),
  })
  const d = await r.json()
  if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`)
  return d
}

async function testProvider(idx: number): Promise<{ status: string; reply?: string; error?: string }> {
  const r = await fetch(`/api/providers/${idx}/test`, { method: 'POST' })
  return r.json()
}

async function revealProviderKey(idx: number): Promise<string> {
  try {
    const r = await fetch(`/api/providers/${idx}/reveal`, { method: 'POST' })
    if (!r.ok) return ''
    const d = await r.json()
    return typeof d.value === 'string' ? d.value : ''
  } catch {
    return ''
  }
}

// ---------------------------------------------------------------------------
// Sortable row
// ---------------------------------------------------------------------------

interface SortableRowProps {
  provider: ProviderView
  onEdit: (p: ProviderView) => void
  onDelete: (p: ProviderView) => void
  onToggle: (p: ProviderView) => void
  onTest: (p: ProviderView) => void
  testState: { busy: boolean; result: { ok: boolean; text: string } | null }
}

function SortableRow({ provider, onEdit, onDelete, onToggle, onTest, testState }: SortableRowProps) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: provider.name,
  })

  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.5 : 1,
  }

  const maskedURL = provider.base_url
    ? provider.base_url.length > 36
      ? provider.base_url.slice(0, 36) + '…'
      : provider.base_url
    : '(api.anthropic.com)'

  return (
    <div
      ref={setNodeRef}
      style={style}
      className={cn(
        'flex items-center gap-2 px-3 py-2 rounded border text-sm',
        provider.enabled
          ? 'bg-card border-border'
          : 'bg-muted/40 border-border/50 text-muted-foreground',
        isDragging && 'shadow-lg z-10',
      )}
    >
      {/* Drag handle */}
      <button
        type="button"
        {...attributes}
        {...listeners}
        className="text-muted-foreground/50 hover:text-muted-foreground cursor-grab active:cursor-grabbing shrink-0"
        aria-label="Drag to reorder"
      >
        <GripVertical className="w-4 h-4" />
      </button>

      {/* Priority badge */}
      <span className="text-[10px] font-mono text-muted-foreground w-5 shrink-0">
        {provider.index}
      </span>

      {/* Name */}
      <span className="font-medium w-32 shrink-0 truncate" title={provider.name}>
        {provider.name}
      </span>

      {/* Base URL */}
      <span className="text-muted-foreground font-mono text-xs flex-1 truncate" title={provider.base_url}>
        {maskedURL}
      </span>

      {/* Models */}
      <span className="text-muted-foreground text-xs w-36 shrink-0 truncate" title={`chat:${provider.chat_model||'default'} eval:${provider.eval_model||'default'} op:${provider.operator_model||'default'}`}>
        {[
          provider.chat_model || provider.chat_model_default,
          provider.eval_model || provider.eval_model_default,
          provider.operator_model || provider.operator_model_default,
        ].join(' / ')}
      </span>

      {/* Key hint */}
      <span className="text-muted-foreground font-mono text-xs w-16 shrink-0">
        {provider.api_key_set ? `…${provider.api_key_hint ?? ''}` : '(none)'}
      </span>

      {/* Actions */}
      <div className="flex items-center gap-1 shrink-0">
        {/* Enable toggle */}
        <button
          type="button"
          onClick={() => onToggle(provider)}
          className={cn(
            'text-xs px-1.5 py-0.5 rounded border',
            provider.enabled
              ? 'border-green-200 text-green-700 bg-green-50 hover:bg-green-100'
              : 'border-border text-muted-foreground hover:bg-secondary/50',
          )}
          title={provider.enabled ? 'Disable provider' : 'Enable provider'}
          aria-label={provider.enabled ? 'Disable provider' : 'Enable provider'}
        >
          {provider.enabled ? 'on' : 'off'}
        </button>

        {/* Test */}
        <button
          type="button"
          onClick={() => onTest(provider)}
          disabled={testState.busy}
          className="text-muted-foreground hover:text-foreground disabled:opacity-50"
          title="Test connectivity"
          aria-label="Test provider connectivity"
        >
          {testState.busy
            ? <Loader2 className="w-3.5 h-3.5 animate-spin" />
            : <FlaskConical className="w-3.5 h-3.5" />}
        </button>

        {/* Edit */}
        <button
          type="button"
          onClick={() => onEdit(provider)}
          className="text-muted-foreground hover:text-foreground"
          title="Edit provider"
          aria-label="Edit provider"
        >
          <Pencil className="w-3.5 h-3.5" />
        </button>

        {/* Delete */}
        <button
          type="button"
          onClick={() => onDelete(provider)}
          className="text-muted-foreground hover:text-red-600"
          title="Delete provider"
          aria-label="Delete provider"
        >
          <Trash2 className="w-3.5 h-3.5" />
        </button>
      </div>

      {/* Test result inline */}
      {testState.result && (
        <span
          className={cn(
            'inline-flex items-center gap-1 text-xs px-1.5 py-0.5 rounded border ml-1',
            testState.result.ok
              ? 'bg-green-50 border-green-200 text-green-700'
              : 'bg-red-50 border-red-200 text-red-700',
          )}
        >
          {testState.result.ok
            ? <Check className="w-3 h-3" />
            : <AlertCircle className="w-3 h-3" />}
          {testState.result.text}
        </span>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Add / Edit modal
// ---------------------------------------------------------------------------

interface ProviderModalProps {
  initial?: ProviderView
  onSave: (data: {
    name: string
    base_url: string
    api_key: string
    chat_model: string
    eval_model: string
    operator_model: string
    enabled: boolean
  }) => Promise<void>
  onClose: () => void
}

function ProviderModal({ initial, onSave, onClose }: ProviderModalProps) {
  const [name, setName] = useState(initial?.name ?? '')
  const [baseURL, setBaseURL] = useState(initial?.base_url ?? '')
  const [apiKey, setApiKey] = useState('')
  const [showKey, setShowKey] = useState(false)
  const [chatModel, setChatModel] = useState(initial?.chat_model ?? '')
  const [evalModel, setEvalModel] = useState(initial?.eval_model ?? '')
  const [operatorModel, setOperatorModel] = useState(initial?.operator_model ?? '')
  const [enabled, setEnabled] = useState(initial?.enabled ?? true)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const nameRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    nameRef.current?.focus()
  }, [])

  const handleReveal = async () => {
    if (!showKey && apiKey === '' && initial?.api_key_set && initial.index !== undefined) {
      const v = await revealProviderKey(initial.index)
      if (v) setApiKey(v)
    }
    setShowKey(s => !s)
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!name.trim()) { setErr('Name is required'); return }
    setBusy(true); setErr(null)
    try {
      await onSave({ name: name.trim(), base_url: baseURL.trim(), api_key: apiKey, chat_model: chatModel.trim(), eval_model: evalModel.trim(), operator_model: operatorModel.trim(), enabled })
      onClose()
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      className="fixed inset-0 bg-black/50 flex items-center justify-center z-50"
      onClick={onClose}
      role="dialog"
      aria-modal="true"
      aria-label={initial ? 'Edit provider' : 'Add provider'}
    >
      <div
        className="bg-card border border-border rounded-lg shadow-lg w-full max-w-md p-5 space-y-4"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold">{initial ? 'Edit Provider' : 'Add Provider'}</h3>
          <button type="button" onClick={onClose} className="text-muted-foreground hover:text-foreground" aria-label="Close">
            <X className="w-4 h-4" />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="space-y-3">
          <Field label="Name *">
            <input
              ref={nameRef}
              type="text"
              value={name}
              onChange={e => setName(e.target.value)}
              placeholder="e.g. Anthropic Official"
              className="flex-1 text-sm px-2 py-1 border border-border rounded bg-background"
              required
            />
          </Field>

          <Field label="Base URL">
            <input
              type="url"
              value={baseURL}
              onChange={e => setBaseURL(e.target.value)}
              placeholder="https://api.anthropic.com (default)"
              className="flex-1 text-sm font-mono px-2 py-1 border border-border rounded bg-background"
            />
          </Field>

          <Field label="API Key">
            <div className="flex-1 flex gap-2 items-center">
              <input
                type={showKey ? 'text' : 'password'}
                value={apiKey}
                onChange={e => setApiKey(e.target.value)}
                placeholder={initial?.api_key_set ? `configured · ${initial.api_key_hint ?? ''}` : 'sk-ant-…'}
                className="flex-1 text-sm font-mono px-2 py-1 border border-border rounded bg-background"
                autoComplete="off"
              />
              <button
                type="button"
                onClick={handleReveal}
                className="text-muted-foreground hover:text-foreground"
                title={showKey ? 'Hide' : 'Show saved value'}
                aria-label={showKey ? 'Hide API key' : 'Show API key'}
              >
                {showKey ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
              </button>
            </div>
            {initial?.api_key_set && apiKey === '' && (
              <p className="text-[11px] text-muted-foreground">Leave blank to keep existing key.</p>
            )}
          </Field>

          <Field label="Chat model" hint={`default: ${initial?.chat_model_default ?? 'haiku'}`}>
            <ModelSelect
              value={chatModel}
              onChange={setChatModel}
              defaultLabel={initial?.chat_model_default ?? 'haiku'}
              ariaLabel="Chat model"
            />
          </Field>

          <Field label="Eval model" hint={`default: ${initial?.eval_model_default ?? 'opus'}`}>
            <ModelSelect
              value={evalModel}
              onChange={setEvalModel}
              defaultLabel={initial?.eval_model_default ?? 'opus'}
              ariaLabel="Eval model"
            />
          </Field>

          <Field label="Operator model" hint={`default: ${initial?.operator_model_default ?? 'sonnet'}`}>
            <ModelSelect
              value={operatorModel}
              onChange={setOperatorModel}
              defaultLabel={initial?.operator_model_default ?? 'sonnet'}
              ariaLabel="Operator model"
            />
          </Field>

          <div className="flex items-start gap-3">
            <div className="w-28 shrink-0" />
            <p className="flex-1 text-[10px] text-muted-foreground/70 leading-relaxed">
              Type an alias (opus / sonnet / haiku) to always track that tier's
              latest model; type a full model ID to pin that exact version;
              custom providers can enter any model name they expose. Leave blank
              to use the default shown above.
            </p>
          </div>

          <Field label="Enabled">
            <input
              type="checkbox"
              checked={enabled}
              onChange={e => setEnabled(e.target.checked)}
              className="rounded border-border w-4 h-4"
              aria-label="Provider enabled"
            />
          </Field>

          {err && (
            <p className="text-xs text-red-700 bg-red-50 border border-red-200 px-2 py-1.5 rounded">
              {err}
            </p>
          )}

          <div className="flex justify-end gap-2 pt-1">
            <button
              type="button"
              onClick={onClose}
              className="text-sm px-3 py-1 rounded border border-border hover:bg-secondary/50 text-foreground"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={busy}
              className={cn(
                'text-sm px-3 py-1 rounded border',
                !busy
                  ? 'bg-secondary hover:bg-secondary/70 border-border text-foreground'
                  : 'bg-muted text-muted-foreground border-border cursor-not-allowed',
              )}
            >
              {busy ? <Loader2 className="w-3 h-3 animate-spin inline mr-1" /> : null}
              {initial ? 'Save' : 'Add'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

// ModelSelect is a single editable combobox: type any custom model ID, or
// click the chevron to pick an alias suggestion. We hand-roll the dropdown
// instead of using a native <datalist> because the native control filters
// suggestions by the current input — once the field already holds an alias
// (e.g. "opus"), the popup collapses to that one match and looks like there
// is nothing else to choose, and its arrow/popup styling can't be themed.
// This custom list always shows the full alias set when open. Empty value
// keeps the "inherit default" semantics the backend expects.
function ModelSelect({
  value, onChange, defaultLabel, ariaLabel,
}: {
  value: string
  onChange: (v: string) => void
  defaultLabel: string
  ariaLabel: string
}) {
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement>(null)

  // Close the dropdown when the user clicks anywhere outside the combobox.
  useEffect(() => {
    if (!open) return
    function onDocMouseDown(e: MouseEvent) {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', onDocMouseDown)
    return () => document.removeEventListener('mousedown', onDocMouseDown)
  }, [open])

  return (
    <div ref={wrapRef} className="flex-1 flex flex-col gap-1 relative">
      <div className="relative">
        <input
          type="text"
          value={value}
          onChange={e => onChange(e.target.value)}
          onFocus={() => setOpen(true)}
          placeholder={`default — ${defaultLabel}`}
          className="w-full text-sm font-mono px-2 py-1 pr-7 border border-border rounded bg-background"
          aria-label={ariaLabel}
        />
        <button
          type="button"
          tabIndex={-1}
          onClick={() => setOpen(o => !o)}
          className="absolute right-1 top-1/2 -translate-y-1/2 p-0.5 text-muted-foreground hover:text-foreground"
          aria-label={`Toggle ${ariaLabel} suggestions`}
        >
          <ChevronDown className="w-4 h-4" />
        </button>
      </div>
      {open && (
        <ul
          role="listbox"
          className="absolute z-10 top-full mt-1 w-full bg-background border border-border rounded shadow-md py-1 max-h-48 overflow-auto"
        >
          {MODEL_PRESETS.map(m => (
            <li key={m}>
              <button
                type="button"
                role="option"
                aria-selected={value === m}
                onClick={() => { onChange(m); setOpen(false) }}
                className={cn(
                  'w-full text-left text-sm font-mono px-2 py-1 hover:bg-secondary/60',
                  value === m && 'bg-secondary/40',
                )}
              >
                {m}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-3">
      <div className="w-28 shrink-0 pt-1.5">
        <label className="text-xs text-muted-foreground">{label}</label>
        {hint && <p className="text-[10px] text-muted-foreground/70 mt-0.5">{hint}</p>}
      </div>
      <div className="flex-1 flex flex-col gap-1">{children}</div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Main ProvidersSection
// ---------------------------------------------------------------------------

export function ProvidersSection() {
  const [providers, setProviders] = useState<ProviderView[]>([])
  const [loading, setLoading] = useState(true)
  const [loadErr, setLoadErr] = useState<string | null>(null)
  const [modal, setModal] = useState<{ mode: 'add' | 'edit'; provider?: ProviderView } | null>(null)
  const [testStates, setTestStates] = useState<
    Record<string, { busy: boolean; result: { ok: boolean; text: string } | null }>
  >({})
  const [saveErr, setSaveErr] = useState<string | null>(null)

  const sensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )

  const load = useCallback(() => {
    setLoadErr(null)
    fetchProviders()
      .then(ps => setProviders(ps))
      .catch(e => setLoadErr(String(e.message || e)))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])

  const handleDragEnd = async (event: DragEndEvent) => {
    const { active, over } = event
    if (!over || active.id === over.id) return

    const oldIndex = providers.findIndex(p => p.name === active.id)
    const newIndex = providers.findIndex(p => p.name === over.id)
    const reordered = arrayMove(providers, oldIndex, newIndex).map((p, i) => ({ ...p, index: i }))
    setProviders(reordered) // optimistic update

    try {
      const updated = await reorderProviders(reordered.map(p => p.name))
      setProviders(updated)
    } catch (e: unknown) {
      setSaveErr(e instanceof Error ? e.message : String(e))
      load() // revert on error
    }
  }

  const handleToggle = async (p: ProviderView) => {
    try {
      const updated = await updateProvider(p.index, { enabled: !p.enabled })
      setProviders(prev => prev.map(x => (x.name === p.name ? updated : x)))
    } catch (e: unknown) {
      setSaveErr(e instanceof Error ? e.message : String(e))
    }
  }

  const handleDelete = async (p: ProviderView) => {
    if (!window.confirm(`Delete provider "${p.name}"?`)) return
    try {
      await deleteProvider(p.index)
      load()
    } catch (e: unknown) {
      setSaveErr(e instanceof Error ? e.message : String(e))
    }
  }

  const handleTest = async (p: ProviderView) => {
    setTestStates(prev => ({ ...prev, [p.name]: { busy: true, result: null } }))
    try {
      const d = await testProvider(p.index)
      const ok = d.status === 'ok'
      const text = ok
        ? `OK${d.reply ? ' — ' + d.reply.slice(0, 60) : ''}`
        : (d.error || 'failed').slice(0, 80)
      setTestStates(prev => ({ ...prev, [p.name]: { busy: false, result: { ok, text } } }))
    } catch (e: unknown) {
      setTestStates(prev => ({
        ...prev,
        [p.name]: { busy: false, result: { ok: false, text: e instanceof Error ? e.message : String(e) } },
      }))
    }
  }

  const handleSaveAdd = async (data: {
    name: string; base_url: string; api_key: string; chat_model: string; eval_model: string; operator_model: string; enabled: boolean
  }) => {
    await addProvider({
      name: data.name,
      base_url: data.base_url || undefined,
      api_key: data.api_key || undefined,
      chat_model: data.chat_model || undefined,
      eval_model: data.eval_model || undefined,
      operator_model: data.operator_model || undefined,
      enabled: data.enabled,
    })
    load()
  }

  const handleSaveEdit = (p: ProviderView) => async (data: {
    name: string; base_url: string; api_key: string; chat_model: string; eval_model: string; operator_model: string; enabled: boolean
  }) => {
    const body: Record<string, unknown> = {
      name: data.name,
      base_url: data.base_url,
      chat_model: data.chat_model,
      eval_model: data.eval_model,
      operator_model: data.operator_model,
      enabled: data.enabled,
    }
    // Only send api_key if the user typed something (non-empty = replace).
    if (data.api_key !== '') {
      body.api_key = data.api_key
    }
    await updateProvider(p.index, body as Parameters<typeof updateProvider>[1])
    load()
  }

  return (
    <section className="bg-card border border-border rounded-xl p-4 space-y-3">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-sm font-semibold text-foreground">Claude API Providers</h2>
          <p className="text-xs text-muted-foreground mt-0.5">
            Ordered list of Claude API configurations. The runner tries providers top-to-bottom,
            failing over on rate limits, auth errors, and network failures.
            Drag to reorder — index 0 has highest priority.
          </p>
        </div>
        <button
          type="button"
          onClick={() => setModal({ mode: 'add' })}
          className="text-sm px-2.5 py-1 rounded border border-border hover:bg-secondary/50 text-foreground inline-flex items-center gap-1.5 shrink-0"
          aria-label="Add provider"
        >
          <Plus className="w-3.5 h-3.5" />
          Add Provider
        </button>
      </div>

      {loading && <p className="text-xs text-muted-foreground">Loading…</p>}
      {loadErr && (
        <div className="text-xs text-red-700 bg-red-50 border border-red-200 px-2 py-1.5 rounded">
          Failed to load providers: {loadErr}
        </div>
      )}
      {saveErr && (
        <div className="text-xs text-red-700 bg-red-50 border border-red-200 px-2 py-1.5 rounded flex items-center justify-between">
          <span>{saveErr}</span>
          <button type="button" onClick={() => setSaveErr(null)} aria-label="Dismiss error">
            <X className="w-3 h-3" />
          </button>
        </div>
      )}

      {!loading && providers.length === 0 && !loadErr && (
        <p className="text-xs text-muted-foreground py-2">
          No providers configured. Add one to enable multi-provider failover.
        </p>
      )}

      {providers.length > 0 && (
        <>
          {/* Column headers */}
          <div className="flex items-center gap-2 px-3 text-[10px] text-muted-foreground font-medium uppercase tracking-wide">
            <span className="w-4 shrink-0" />
            <span className="w-5 shrink-0">#</span>
            <span className="w-32 shrink-0">Name</span>
            <span className="flex-1">Base URL</span>
            <span className="w-36 shrink-0">Models (chat/eval/op)</span>
            <span className="w-16 shrink-0">Key</span>
            <span className="w-28 shrink-0">Actions</span>
          </div>

          <DndContext
            sensors={sensors}
            collisionDetection={closestCenter}
            onDragEnd={handleDragEnd}
          >
            <SortableContext
              items={providers.map(p => p.name)}
              strategy={verticalListSortingStrategy}
            >
              <div className="space-y-1">
                {providers.map(p => (
                  <SortableRow
                    key={p.name}
                    provider={p}
                    onEdit={p => setModal({ mode: 'edit', provider: p })}
                    onDelete={handleDelete}
                    onToggle={handleToggle}
                    onTest={handleTest}
                    testState={testStates[p.name] ?? { busy: false, result: null }}
                  />
                ))}
              </div>
            </SortableContext>
          </DndContext>
        </>
      )}

      {modal && (
        <ProviderModal
          initial={modal.mode === 'edit' ? modal.provider : undefined}
          onSave={modal.mode === 'add' ? handleSaveAdd : handleSaveEdit(modal.provider!)}
          onClose={() => setModal(null)}
        />
      )}
    </section>
  )
}
