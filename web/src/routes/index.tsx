import { createFileRoute } from '@tanstack/react-router'
import { ArrowRight, Bot, Check, Copy, ExternalLink, Terminal } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

const INSTALL_COMMAND = 'curl -fsSL https://raw.githubusercontent.com/zhoushoujianwork/clawflow/main/get.sh | bash'
const SKILL_COMMAND = 'clawflow install-skill'

type TerminalTone = 'ai' | 'command' | 'error' | 'success' | 'warning' | 'muted' | 'normal'

const CI_TERMINAL_LINES: Array<{ text: string; tone?: TerminalTone }> = [
  { text: 'AI > check the latest GitLab smoke failure', tone: 'ai' },
  { text: '' },
  { text: '$ clawflow ci pipeline list --repo group/app --ref release/v0.0.0-smoke --status failed', tone: 'command' },
  { text: 'pipeline #989119  failed  sha 98f1c802...', tone: 'error' },
  { text: '' },
  { text: '$ clawflow ci pipeline view --repo group/app --id 989119', tone: 'command' },
  { text: 'test     success', tone: 'success' },
  { text: 'build    success', tone: 'success' },
  { text: 'release  failed', tone: 'error' },
  { text: '' },
  { text: '$ clawflow ci job log --repo group/app --job 10724643', tone: 'command' },
  { text: 'sh scripts/ci-release.sh', tone: 'muted' },
  { text: 'bad decrypt', tone: 'error' },
  { text: '' },
  { text: 'AI verdict:', tone: 'ai' },
  { text: 'release is using an old encrypted variable format.', tone: 'normal' },
  { text: 'Rotate CBS_CLI_YAML_ENC_B64 and SKILLHUB_TOKEN_ENC_B64.', tone: 'warning' },
  { text: 'Keep CI_RELEASE_CONFIG_KEY unchanged, then retry release only.', tone: 'success' },
]

export const Route = createFileRoute('/')({
  component: LandingPage,
})

function LandingPage() {
  const [copied, setCopied] = useState<'install' | 'skill' | null>(null)

  const copy = async (kind: 'install' | 'skill', command: string) => {
    try {
      await navigator.clipboard.writeText(command)
      setCopied(kind)
      window.setTimeout(() => setCopied(null), 1800)
    } catch {
      setCopied(null)
    }
  }

  return (
    <main className="min-h-screen" style={{ background: 'hsl(var(--bg-primary))', color: 'hsl(var(--text-normal))' }}>
      <header
        className="border-b"
        style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-primary))' }}
      >
        <div className="mx-auto flex h-14 max-w-6xl items-center justify-between px-5">
          <a href="/" className="flex items-center gap-2" aria-label="ClawFlow home">
            <span className="flex h-8 w-8 items-center justify-center rounded-sm bg-[#111111]">
              <svg viewBox="0 0 24 24" className="h-4 w-4" aria-hidden="true">
                <g stroke="#e8792a" strokeWidth="2.3" strokeLinecap="round" fill="none">
                  <path d="M4,20 Q6,13 9,4" />
                  <path d="M10,20 Q12,13 15,4" />
                  <path d="M16,20 Q18,13 21,4" />
                </g>
              </svg>
            </span>
            <span className="text-sm font-semibold" style={{ color: 'hsl(var(--text-high))' }}>
              <span style={{ color: 'hsl(var(--brand))' }}>Claw</span>Flow
            </span>
          </a>
          <nav className="flex items-center gap-2">
            <a
              href="https://github.com/zhoushoujianwork/clawflow"
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex h-8 w-8 items-center justify-center rounded-sm border"
              style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}
              aria-label="GitHub repository"
              title="GitHub repository"
            >
              <ExternalLink className="h-4 w-4" />
            </a>
            <a
              href="https://github.com/zhoushoujianwork/clawflow"
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex h-8 items-center gap-1 rounded-sm px-3 text-sm font-medium"
              style={{ background: 'hsl(var(--brand))', color: 'hsl(var(--text-on-brand))' }}
            >
              Open GitHub
              <ArrowRight className="h-3.5 w-3.5" />
            </a>
          </nav>
        </div>
      </header>

      <section className="mx-auto grid max-w-6xl gap-8 px-5 py-10 md:grid-cols-[minmax(0,1fr)_420px] md:items-center md:py-16">
        <div className="space-y-7">
          <div className="inline-flex items-center gap-2 rounded-sm border px-2.5 py-1 text-xs font-medium" style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}>
            <Bot className="h-3.5 w-3.5" />
            AI coding tools ready after install
          </div>

          <div className="space-y-4">
            <h1 className="max-w-3xl text-4xl font-semibold leading-tight md:text-5xl" style={{ color: 'hsl(var(--text-high))' }}>
              ClawFlow turns issues into reviewed pull requests.
            </h1>
            <p className="max-w-2xl text-base leading-7 md:text-lg" style={{ color: 'hsl(var(--text-low))' }}>
              Label-driven automation for GitHub and GitLab. ClawFlow installs a local CLI, ships built-in operators, and writes the agent skill into detected AI tools so assistants like Claude Code, Codex, Cursor, and Windsurf know how to run your workflow.
            </p>
          </div>

          <div className="grid gap-3 sm:grid-cols-3">
            <Feature label="No SaaS backend" value="State stays in labels and comments." />
            <Feature label="Native VCS flow" value="Issues become reviewed PRs." />
            <Feature label="AI tool ready" value="Installs the skill for your coding assistants." />
          </div>
        </div>

        <div
          id="install"
          className="rounded-sm border p-4 shadow-sm"
          style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-secondary))' }}
          aria-label="AI one-click install"
        >
          <div className="mb-4 flex items-start justify-between gap-3">
            <div>
              <div className="flex items-center gap-2 text-sm font-semibold" style={{ color: 'hsl(var(--text-high))' }}>
                <Terminal className="h-4 w-4" />
                AI one-click install
              </div>
              <p className="mt-1 text-sm leading-6" style={{ color: 'hsl(var(--text-low))' }}>
                Installs the binary, creates the config, and automatically installs the ClawFlow skill into detected AI coding tools.
              </p>
            </div>
          </div>

          <CommandBlock
            label="Install CLI + AI skill"
            command={INSTALL_COMMAND}
            copied={copied === 'install'}
            onCopy={() => copy('install', INSTALL_COMMAND)}
          />

          <div className="mt-4 rounded-sm border p-3" style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-primary))' }}>
            <p className="text-xs font-semibold uppercase tracking-wide" style={{ color: 'hsl(var(--text-low))' }}>
              Skill refresh
            </p>
            <p className="mt-1 text-sm leading-6" style={{ color: 'hsl(var(--text-normal))' }}>
              Already installed ClawFlow? Re-run the skill installer to update AI tool instructions only.
            </p>
            <CommandBlock
              label="Install or refresh AI skill"
              command={SKILL_COMMAND}
              copied={copied === 'skill'}
              onCopy={() => copy('skill', SKILL_COMMAND)}
              compact
            />
          </div>
        </div>
      </section>

      <section className="border-t" style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-primary))' }}>
        <div className="mx-auto grid max-w-6xl gap-6 px-5 py-8 md:grid-cols-[minmax(0,1fr)_440px] md:items-start">
          <div className="space-y-5">
            <div className="inline-flex items-center gap-2 rounded-sm border px-2.5 py-1 text-xs font-medium" style={{ borderColor: 'hsl(var(--border))', color: 'hsl(var(--text-low))' }}>
              <Terminal className="h-3.5 w-3.5" />
              Best practice · GitLab CI
            </div>
            <div className="space-y-3">
              <h2 className="text-2xl font-semibold leading-tight md:text-3xl" style={{ color: 'hsl(var(--text-high))' }}>
                Let AI call ClawFlow, inspect GitLab CI, and explain the fix.
              </h2>
              <p className="max-w-2xl text-sm leading-6 md:text-base" style={{ color: 'hsl(var(--text-low))' }}>
                In Claude Code, Codex, Cursor, or Windsurf, the installed ClawFlow skill lets AI run <code className="rounded-sm px-1 py-0.5" style={{ background: 'hsl(var(--bg-secondary))', color: 'hsl(var(--text-high))' }}>clawflow ci</code>, fetch GitLab pipeline traces, compare job status, and return the failing line plus the safest retry action.
              </p>
            </div>
            <div className="grid gap-3 sm:grid-cols-3">
              <Feature label="GitLab pipeline" value="Query failed pipelines by ref, status, and SHA." />
              <Feature label="Terminal trace" value="Render the tool calls and returned job log." />
              <Feature label="AI verdict" value="Summarize root cause, variables, and retry step." />
            </div>
          </div>

          <TerminalReplay />
        </div>
      </section>

      <section className="border-t" style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-secondary))' }}>
        <div className="mx-auto grid max-w-6xl gap-4 px-5 py-8 md:grid-cols-4">
          <Step n="1" title="Install once" body="The installer downloads the CLI and installs the AI skill." />
          <Step n="2" title="Add repositories" body="Point ClawFlow at GitHub, GitLab, or a local checkout." />
          <Step n="3" title="Use labels" body="Labels decide which operator evaluates or implements an issue." />
          <Step n="4" title="Review the PR" body="Check the generated branch, comments, and pull request in your VCS." />
        </div>
      </section>
    </main>
  )
}

function TerminalReplay() {
  const [visibleCount, setVisibleCount] = useState(1)
  const scrollRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const reduceMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    if (reduceMotion) {
      setVisibleCount(CI_TERMINAL_LINES.length)
      return
    }

    const interval = window.setInterval(() => {
      setVisibleCount((current) => {
        if (current >= CI_TERMINAL_LINES.length + 8) return 1
        return current + 1
      })
    }, 520)

    return () => window.clearInterval(interval)
  }, [])

  useEffect(() => {
    const viewport = scrollRef.current
    if (!viewport) return
    viewport.scrollTop = viewport.scrollHeight
  }, [visibleCount])

  const visibleLines = CI_TERMINAL_LINES.slice(0, Math.min(visibleCount, CI_TERMINAL_LINES.length))
  const cursorVisible = visibleCount <= CI_TERMINAL_LINES.length

  return (
    <div className="overflow-hidden rounded-sm border shadow-sm" style={{ borderColor: 'hsl(var(--border))', background: '#101216' }}>
      <div className="flex h-10 items-center justify-between gap-3 border-b px-3" style={{ borderColor: 'rgba(255,255,255,0.08)', background: '#202124' }}>
        <div className="flex items-center gap-2">
          <span className="h-3 w-3 rounded-full bg-[#ff5f57]" aria-hidden="true" />
          <span className="h-3 w-3 rounded-full bg-[#ffbd2e]" aria-hidden="true" />
          <span className="h-3 w-3 rounded-full bg-[#28c840]" aria-hidden="true" />
        </div>
        <div className="min-w-0 flex-1 truncate text-center font-mono text-[11px]" style={{ color: '#d4d4d8' }}>
          gitlab-ci - clawflow
        </div>
        <div className="hidden items-center gap-1.5 text-[11px] sm:flex">
          <span className="rounded-sm border px-1.5 py-0.5" style={{ borderColor: 'rgba(255,255,255,0.18)', color: '#d4d4d8' }}>pipeline #989119</span>
          <span className="rounded-sm border px-1.5 py-0.5" style={{ borderColor: 'rgba(248,113,113,0.45)', color: '#fca5a5' }}>failed</span>
        </div>
      </div>
      <div
        ref={scrollRef}
        className="h-[360px] overflow-y-auto p-4 font-mono text-[11px] leading-6 sm:text-xs"
        style={{ background: '#0f1115' }}
        aria-label="Animated GitLab CI terminal session"
      >
        <div className="mb-3 flex items-center gap-1.5 text-[11px] sm:hidden">
          <span className="rounded-sm border px-1.5 py-0.5" style={{ borderColor: 'rgba(255,255,255,0.18)', color: '#d4d4d8' }}>pipeline #989119</span>
          <span className="rounded-sm border px-1.5 py-0.5" style={{ borderColor: 'rgba(248,113,113,0.45)', color: '#fca5a5' }}>failed</span>
        </div>
        <div role="log" aria-live="polite" aria-atomic="false">
          {visibleLines.map((line, index) => (
            <div
              key={`${line.text}-${index}`}
              className="min-h-6 whitespace-pre-wrap break-words"
              style={{ color: terminalToneColor(line.tone) }}
            >
              {line.text || '\u00a0'}
            </div>
          ))}
          {cursorVisible && (
            <span className="inline-block h-4 w-2 animate-pulse align-middle" style={{ background: '#d4d4d8' }} aria-hidden="true" />
          )}
        </div>
      </div>
    </div>
  )
}

function terminalToneColor(tone: TerminalTone = 'normal') {
  switch (tone) {
    case 'ai':
      return '#fbbf24'
    case 'command':
      return '#93c5fd'
    case 'error':
      return '#fca5a5'
    case 'success':
      return '#86efac'
    case 'warning':
      return '#fde68a'
    case 'muted':
      return '#a1a1aa'
    default:
      return '#f5f5f5'
  }
}

function Feature({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-sm border p-3" style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-secondary))' }}>
      <p className="text-sm font-semibold" style={{ color: 'hsl(var(--text-high))' }}>{label}</p>
      <p className="mt-1 text-sm leading-5" style={{ color: 'hsl(var(--text-low))' }}>{value}</p>
    </div>
  )
}

function CommandBlock({
  label,
  command,
  copied,
  onCopy,
  compact = false,
}: {
  label: string
  command: string
  copied: boolean
  onCopy: () => void
  compact?: boolean
}) {
  return (
    <div className={compact ? 'mt-3' : ''}>
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <span className="text-xs font-medium" style={{ color: 'hsl(var(--text-low))' }}>{label}</span>
        <button
          type="button"
          onClick={onCopy}
          className="inline-flex h-7 w-7 items-center justify-center rounded-sm border"
          style={{ borderColor: 'hsl(var(--border))', color: copied ? 'hsl(var(--success))' : 'hsl(var(--text-low))' }}
          aria-label={`Copy ${label}`}
          title={`Copy ${label}`}
        >
          {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
        </button>
      </div>
      <pre
        className="overflow-hidden whitespace-pre-wrap break-all rounded-sm border p-3 text-xs leading-6"
        style={{ borderColor: 'hsl(var(--border))', background: '#111111', color: '#f5f5f5' }}
      >
        <code>{command}</code>
      </pre>
    </div>
  )
}

function Step({ n, title, body }: { n: string; title: string; body: string }) {
  return (
    <div className="rounded-sm border p-4" style={{ borderColor: 'hsl(var(--border))', background: 'hsl(var(--bg-primary))' }}>
      <div
        className="mb-3 flex h-7 w-7 items-center justify-center rounded-sm text-sm font-semibold"
        style={{ background: 'hsl(var(--brand) / 0.12)', color: 'hsl(var(--brand))' }}
      >
        {n}
      </div>
      <h2 className="text-base font-semibold" style={{ color: 'hsl(var(--text-high))' }}>{title}</h2>
      <p className="mt-1 text-sm leading-6" style={{ color: 'hsl(var(--text-low))' }}>{body}</p>
    </div>
  )
}
