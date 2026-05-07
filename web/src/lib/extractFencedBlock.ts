// extractLastFencedBlock mirrors internal/chat/project.go's extractFencedBlock.
//
// Scans `text` line-by-line for a fenced code block whose info string matches
// `infoString`, returning the body of the LAST such block. Returns "" if no
// matching block is found.
//
// Recognized openers (matching the Go side):
//   ```<infoString>
//   ``` <infoString>
//   ~~~ <infoString>
// Closer is a bare ``` or ~~~ on its own line.
//
// The fence must terminate with at most one trailing whitespace char
// after the info string — same heuristic as the Go reference, so we don't
// accidentally match e.g. ```contextfoo when looking for `context.md`.

export function extractLastFencedBlock(text: string, infoString: string): string {
  if (!text || !infoString) return ''

  const lines = text.split('\n')
  let last = ''
  let inBlock = false
  let blockLines: string[] = []

  for (const line of lines) {
    const trimmed = line.trim()
    if (!inBlock) {
      const tightBack = '```' + infoString
      const looseBack = '``` ' + infoString
      const looseTilde = '~~~ ' + infoString
      const matchTight = trimmed.startsWith(tightBack) && trimmed.length <= tightBack.length + 1
      const matchLooseBack = trimmed.startsWith(looseBack)
      const matchLooseTilde = trimmed.startsWith(looseTilde)
      if (matchTight || matchLooseBack || matchLooseTilde) {
        inBlock = true
        blockLines = []
      }
      continue
    }
    if (trimmed === '```' || trimmed === '~~~') {
      inBlock = false
      last = blockLines.join('\n')
      continue
    }
    blockLines.push(line)
  }

  return last
}
