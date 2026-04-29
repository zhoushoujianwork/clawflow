import { useEffect, useRef, type MutableRefObject } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { WS_CLOSE_DESTROY, WS_CLOSE_COLLAPSE, type CloseIntent } from './ChatDrawer'

interface TerminalProps {
  wsUrl: string
  // Optional: if provided, the WS close on unmount uses a 4xxx code
  // matching the ref's current value. The server reads this to decide
  // whether to delete the session transcript (destroy) or keep it for
  // the next --resume (collapse).
  closeIntentRef?: MutableRefObject<CloseIntent>
}

export function XTerminal({ wsUrl, closeIntentRef }: TerminalProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)

  useEffect(() => {
    if (!containerRef.current) return

    const term = new Terminal({
      cursorBlink: true,
      // Block cursor matches what claude-CLI's TUI expects; without an
      // explicit style xterm renders a thin underline that's easy to
      // miss against the dark background.
      cursorStyle: 'block',
      fontSize: 13,
      fontFamily: '"IBM Plex Mono", "Fira Code", monospace',
      theme: {
        background: '#1a1a2e',
        foreground: '#e0e0e0',
        cursor: '#e8792a',
        selectionBackground: '#e8792a33',
      },
    })
    termRef.current = term

    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.loadAddon(new WebLinksAddon())

    term.open(containerRef.current)
    fitAddon.fit()
    // xterm only animates cursorBlink while the terminal element has
    // focus. Auto-focus on mount so a freshly opened drawer shows a
    // blinking cursor without the user having to click into it; also
    // re-focus when the page tab becomes visible again so flipping
    // between tabs doesn't leave a frozen cursor behind.
    term.focus()
    const onVisibility = () => {
      if (document.visibilityState === 'visible') term.focus()
    }
    document.addEventListener('visibilitychange', onVisibility)

    const ws = new WebSocket(wsUrl)
    ws.binaryType = 'arraybuffer'

    ws.onopen = () => {
      term.writeln('\x1b[33mConnected to ClawFlow chat\x1b[0m\r\n')
    }

    ws.onmessage = (event) => {
      const data = event.data instanceof ArrayBuffer
        ? new Uint8Array(event.data)
        : event.data
      term.write(data)
    }

    ws.onclose = () => {
      term.writeln('\r\n\x1b[31mSession ended\x1b[0m')
    }

    ws.onerror = () => {
      term.writeln('\r\n\x1b[31mConnection error\x1b[0m')
    }

    term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(data)
      }
    })

    const safeFit = () => {
      try { fitAddon.fit() } catch { /* container detached, ignore */ }
    }
    const onResize = () => safeFit()
    window.addEventListener('resize', onResize)

    // Drawer width is user-resizable, so window resize alone isn't
    // enough — the container changes size while the window doesn't.
    const ro = new ResizeObserver(() => safeFit())
    ro.observe(containerRef.current)

    return () => {
      window.removeEventListener('resize', onResize)
      document.removeEventListener('visibilitychange', onVisibility)
      ro.disconnect()
      // Tag the close with the user's intent so the server knows
      // whether to wipe the session transcript. Default to collapse
      // when no ref is wired (preserves session — safer default).
      const intent = closeIntentRef?.current ?? 'collapse'
      const code = intent === 'destroy' ? WS_CLOSE_DESTROY : WS_CLOSE_COLLAPSE
      try {
        ws.close(code, intent)
      } catch {
        // If the WS rejects our custom code (rare; not all browsers
        // accept 4xxx codes via close()), fall back to a default close.
        ws.close()
      }
      term.dispose()
    }
  }, [wsUrl])

  return (
    <div
      ref={containerRef}
      className="w-full h-full min-h-0"
      style={{ padding: '4px' }}
    />
  )
}
