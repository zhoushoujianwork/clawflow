import { useEffect } from 'react'

const DEFAULT_TITLE = 'ClawFlow — Automated Issue to PR'

/**
 * Dynamically sets document.title to `${prefix} · ClawFlow` when a prefix
 * is provided, or restores the default title when the component unmounts.
 */
export function useDocumentTitle(prefix?: string) {
  useEffect(() => {
    document.title = prefix ? `${prefix} · ClawFlow` : DEFAULT_TITLE
    return () => {
      document.title = DEFAULT_TITLE
    }
  }, [prefix])
}
