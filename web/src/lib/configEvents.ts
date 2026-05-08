import { useEffect } from 'react'

// Fired after a successful sync pull (or login that triggers a pull) so
// views holding cached config-derived state can re-fetch without a full
// page reload. Pure DOM event — no global state needed.
export const CONFIG_CHANGED_EVENT = 'clawflow:config-changed'

export function emitConfigChanged() {
  window.dispatchEvent(new Event(CONFIG_CHANGED_EVENT))
}

export function useConfigChanged(handler: () => void) {
  useEffect(() => {
    window.addEventListener(CONFIG_CHANGED_EVENT, handler)
    return () => window.removeEventListener(CONFIG_CHANGED_EVENT, handler)
  }, [handler])
}
