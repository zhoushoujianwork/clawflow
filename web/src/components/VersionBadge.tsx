import { Loader2 } from 'lucide-react'

type Props = {
  currentVersion: string
  latestVersion: string | null
  channel: string
  versionDetail: string
  updateAvailable: boolean
 updating: boolean
  restarting: boolean
  showTooltip: boolean
  triggerUpdate: () => void
  setShowTooltip: (show: boolean) => void
}

export function VersionBadge({currentVersion, latestVersion, channel, versionDetail, updateAvailable, updating, restarting, showTooltip, triggerUpdate, setShowTooltip}: Props) {
  return (
    <div className="relative">
      {updateAvailable && <button
        onClick={updateAvailable ? triggerUpdate : undefined}
        onMouseEnter={() => setShowTooltip(true)}
        onMouseLeave={() => setShowTooltip(false)}
        disabled={!updateAvailable || updating || restarting}
        className={`relative inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-mono tabular-nums whitespace-nowrap transition-colors ${
          updateAvailable && !updating && !restarting
            ? 'cursor-pointer hover:bg-[hsl(var(--brand)/0.1)] text-[hsl(var(--text-mid,var(--text-low)))]'
            : 'cursor-default text-[hsl(var(--text-low))]'
        }`}
        style={{ color: 'hsl(var(--text-low))' }}
        aria-label={updateAvailable ? `Upgrade to ${latestVersion}` : `Version ${currentVersion}`}
      >
        {restarting ? (
          <><Loader2 className="w-3 h-3 animate-spin" /> Restarting…</>
        ) : updating ? (
          <><Loader2 className="w-3 h-3 animate-spin" /> Updating…</>
        ) : (
          <>
            {channel} · {currentVersion} {versionDetail}
            {updateAvailable && (
              <svg className="w-2 h-2 shrink-0" viewBox="0 0 8 8" aria-hidden="true">
                <circle cx="4" cy="4" r="4" fill="#ef4444" />
              </svg>
            )}
          </>
        )}
      </button>}
      {!updateAvailable && <span className="text-[11px] font-mono" title={versionDetail}>{channel} · {currentVersion} {versionDetail}</span>}
      {/* Tooltip */}
      {showTooltip && updateAvailable && !updating && !restarting && (
        <div
          className="absolute top-full right-0 mt-1.5 px-2.5 py-1.5 rounded-md text-[11px] whitespace-nowrap shadow-lg z-50 border"
          style={{
            background: 'hsl(var(--bg-panel))',
            borderColor: 'hsl(var(--border))',
            color: 'hsl(var(--text-high))',
          }}
        >
          Click to upgrade to <span className="font-semibold">{latestVersion}</span>
        </div>
      )}
    </div>
  )
}
