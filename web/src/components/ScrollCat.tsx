import { useEffect, useState } from 'react'

export type CatState = 'sleeping' | 'waking' | 'working' | 'celebrating' | 'waving'

const DIALOGUE: Record<CatState, string> = {
  sleeping:    'zzz… issues pile up…',
  waking:      '👀 A new issue spotted!',
  working:     '⌨️ Analyzing & patching…',
  celebrating: '🎉 PR submitted!',
  waving:      "👋 Let's automate!",
}

function OctoCat({ state }: { state: CatState }) {
  const asleep  = state === 'sleeping'
  const wide    = state === 'celebrating'
  const armsUp  = state === 'celebrating'
  const waving  = state === 'waving'

  return (
    <svg
      viewBox="0 0 96 134"
      xmlns="http://www.w3.org/2000/svg"
      className="w-[88px]"
      aria-label={`ClawFlow cat — ${state}`}
    >
      {/* ── Ears ── */}
      <polygon points="14,42 24,10 38,36" fill="#161b22" />
      <polygon points="18,39 24,16 36,34" fill="#e8792a" />
      <polygon points="58,36 72,10 82,42" fill="#161b22" />
      <polygon points="60,34 72,16 78,39" fill="#e8792a" />

      {/* ── Head ── */}
      <circle cx="48" cy="50" r="30" fill="#161b22" />

      {/* ── Eyes sleeping ── */}
      {asleep && (
        <>
          <path d="M30,47 Q36,54 42,47" fill="none" stroke="#444" strokeWidth="2.2" strokeLinecap="round" />
          <path d="M54,47 Q60,54 66,47" fill="none" stroke="#444" strokeWidth="2.2" strokeLinecap="round" />
        </>
      )}

      {/* ── Eyes open ── */}
      {!asleep && (
        <>
          <circle cx="36" cy="46" r={wide ? 9 : 7.5} fill="white" />
          <circle cx="60" cy="46" r={wide ? 9 : 7.5} fill="white" />
          <circle cx="37" cy="47" r={wide ? 5.5 : 4} fill="#0d1117" />
          <circle cx="61" cy="47" r={wide ? 5.5 : 4} fill="#0d1117" />
          <circle cx="39" cy="44" r="1.6" fill="white" />
          <circle cx="63" cy="44" r="1.6" fill="white" />
        </>
      )}

      {/* ── Nose ── */}
      <path d="M45,58 L51,58 L48,62 Z" fill="#e8792a" />

      {/* ── Mouth ── */}
      {asleep && (
        <line x1="43" y1="66" x2="53" y2="66" stroke="#444" strokeWidth="1.5" strokeLinecap="round" />
      )}
      {state === 'waking' && (
        <path d="M43,66 Q48,70 53,66" fill="none" stroke="#555" strokeWidth="1.5" strokeLinecap="round" />
      )}
      {state === 'working' && (
        <path d="M43,66 Q48,69 53,66" fill="none" stroke="#555" strokeWidth="1.5" strokeLinecap="round" />
      )}
      {wide && (
        <path d="M40,64 Q48,74 56,64" fill="none" stroke="#e8792a" strokeWidth="2.2" strokeLinecap="round" />
      )}
      {waving && (
        <path d="M42,66 Q48,71 54,66" fill="none" stroke="#555" strokeWidth="1.5" strokeLinecap="round" />
      )}

      {/* ── Whiskers ── */}
      <line x1="4"  y1="52" x2="32" y2="55" stroke="#333" strokeWidth="1.2" />
      <line x1="4"  y1="58" x2="32" y2="57" stroke="#333" strokeWidth="1.2" />
      <line x1="64" y1="55" x2="92" y2="52" stroke="#333" strokeWidth="1.2" />
      <line x1="64" y1="57" x2="92" y2="58" stroke="#333" strokeWidth="1.2" />

      {/* ── Neck + Body ── */}
      <rect x="36" y="77" width="24" height="20" fill="#161b22" />
      <ellipse cx="48" cy="98" rx="22" ry="15" fill="#161b22" />

      {/* ── Left arm ── */}
      {armsUp
        ? <path d="M27,91 Q11,76 18,65" stroke="#161b22" strokeWidth="9" strokeLinecap="round" fill="none" />
        : <path d="M27,94 Q12,108 18,118" stroke="#161b22" strokeWidth="9" strokeLinecap="round" fill="none" />
      }

      {/* ── Right arm ── */}
      {armsUp
        ? <path d="M69,91 Q85,76 78,65" stroke="#161b22" strokeWidth="9" strokeLinecap="round" fill="none" />
        : waving
          ? (
            <path
              d="M69,91 Q85,76 78,65"
              stroke="#161b22"
              strokeWidth="9"
              strokeLinecap="round"
              fill="none"
              style={{
                transformBox: 'fill-box',
                transformOrigin: '69px 91px',
                animation: 'wave-arm 0.75s ease-in-out infinite',
              }}
            />
          )
          : <path d="M69,94 Q84,108 78,118" stroke="#161b22" strokeWidth="9" strokeLinecap="round" fill="none" />
      }

      {/* ── Tentacles (GitHub signature) ── */}
      <path d="M33,111 Q26,124 30,133" stroke="#161b22" strokeWidth="7" strokeLinecap="round" fill="none" />
      <path d="M42,113 Q39,127 43,133" stroke="#161b22" strokeWidth="7" strokeLinecap="round" fill="none" />
      <path d="M54,113 Q57,127 53,133" stroke="#161b22" strokeWidth="7" strokeLinecap="round" fill="none" />
      <path d="M63,111 Q70,124 66,133" stroke="#161b22" strokeWidth="7" strokeLinecap="round" fill="none" />

      {/* ── ZZZ (sleeping) ── */}
      {asleep && (
        <>
          <text x="70" y="30" fontSize="11" fill="#555" style={{ animation: 'float-z 2.4s ease-in-out infinite 0s' }}>z</text>
          <text x="78" y="20" fontSize="14" fill="#444" style={{ animation: 'float-z 2.4s ease-in-out infinite 0.7s' }}>z</text>
          <text x="85" y="10" fontSize="17" fill="#333" style={{ animation: 'float-z 2.4s ease-in-out infinite 1.4s' }}>z</text>
        </>
      )}

      {/* ── Thinking dots (working) ── */}
      {state === 'working' && (
        <>
          <circle cx="74" cy="22" r="3"   fill="#e8792a" style={{ animation: 'think-dot 1.2s ease-in-out infinite 0s' }} />
          <circle cx="82" cy="14" r="3.5" fill="#e8792a" style={{ animation: 'think-dot 1.2s ease-in-out infinite 0.3s' }} />
          <circle cx="91" cy="7"  r="4"   fill="#e8792a" style={{ animation: 'think-dot 1.2s ease-in-out infinite 0.6s' }} />
        </>
      )}

      {/* ── Sparkles (celebrating) ── */}
      {wide && (
        <>
          <text x="4"  y="28" fontSize="14" style={{ animation: 'sparkle 0.9s ease-in-out infinite 0s' }}>✨</text>
          <text x="76" y="28" fontSize="14" style={{ animation: 'sparkle 0.9s ease-in-out infinite 0.45s' }}>✨</text>
        </>
      )}
    </svg>
  )
}

export function ScrollCat() {
  const [state, setState] = useState<CatState>('sleeping')
  const [entered, setEntered] = useState(false)

  useEffect(() => {
    const t = setTimeout(() => setEntered(true), 900)

    const map: Array<{ id: string; state: CatState }> = [
      { id: 'sc-hero',        state: 'sleeping'    },
      { id: 'sc-label',       state: 'waking'      },
      { id: 'sc-agent',       state: 'working'     },
      { id: 'sc-pr',          state: 'celebrating' },
      { id: 'sc-cta',         state: 'waving'      },
    ]

    const obs = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) {
            const found = map.find((m) => m.id === e.target.id)
            if (found) setState(found.state)
          }
        }
      },
      { threshold: 0.45 }
    )

    map.forEach(({ id }) => {
      const el = document.getElementById(id)
      if (el) obs.observe(el)
    })

    return () => {
      clearTimeout(t)
      obs.disconnect()
    }
  }, [])

  return (
    <div
      className={`
        fixed right-5 bottom-6 z-50
        hidden lg:flex flex-col items-center gap-2
        pointer-events-none select-none
        transition-all duration-700
        ${entered ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-6'}
      `}
    >
      {/* Speech bubble */}
      <div
        className="relative px-3 py-2 text-xs text-center max-w-[148px] leading-snug rounded-sm border transition-all duration-400"
        style={{
          background: 'hsl(var(--bg-panel))',
          borderColor: 'hsl(var(--border))',
          color: 'hsl(var(--text-normal))',
          boxShadow: '0 2px 12px rgba(0,0,0,0.18)',
        }}
      >
        {DIALOGUE[state]}
        {/* tail outer */}
        <span
          className="absolute -bottom-[9px] left-1/2 -translate-x-1/2 block w-0 h-0"
          style={{
            borderLeft: '8px solid transparent',
            borderRight: '8px solid transparent',
            borderTop: '9px solid hsl(var(--border))',
          }}
        />
        {/* tail inner fill */}
        <span
          className="absolute -bottom-[7px] left-1/2 -translate-x-1/2 block w-0 h-0"
          style={{
            borderLeft: '7px solid transparent',
            borderRight: '7px solid transparent',
            borderTop: '8px solid hsl(var(--bg-panel))',
          }}
        />
      </div>

      {/* Cat character */}
      <div className={`cat-state-${state}`} style={{ filter: 'drop-shadow(0 4px 12px rgba(0,0,0,0.35))' }}>
        <OctoCat state={state} />
      </div>
    </div>
  )
}
