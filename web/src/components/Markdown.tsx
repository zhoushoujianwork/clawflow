import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { cn } from '../lib/utils'

interface MarkdownProps {
  children: string
  className?: string
}

// Markdown wraps react-markdown with GFM (tables, task lists, strikethrough,
// fenced code) and the project's theme tokens. Every color/background
// references hsl(var(--…)) directly rather than tailwind's `primary`,
// `card`, etc. — that mapping is misleading in this repo (e.g.
// tailwind's `text-primary` resolves to `--bg-primary`, which is pure
// white in light mode and produced unreadable white-on-white links).
//
// Tokens used here (defined in web/src/index.css):
//   --text-high   primary content  (high contrast)
//   --text-normal body content     (default)
//   --text-low    secondary / hint
//   --bg-primary  page bg
//   --bg-secondary chip / pre bg
//   --bg-panel    deeper surface
//   --border      divider
//   --brand       accent / link
//
// All values switch automatically between :root (light) and .dark.
export function Markdown({ children, className }: MarkdownProps) {
  return (
    <div
      className={cn('text-sm leading-relaxed', className)}
      style={{ color: 'hsl(var(--text-normal))' }}
    >
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          h1: ({ children }) => (
            <h1 className="text-xl font-bold mt-4 mb-2 first:mt-0" style={{ color: 'hsl(var(--text-high))' }}>
              {children}
            </h1>
          ),
          h2: ({ children }) => (
            <h2 className="text-lg font-semibold mt-4 mb-2 first:mt-0" style={{ color: 'hsl(var(--text-high))' }}>
              {children}
            </h2>
          ),
          h3: ({ children }) => (
            <h3 className="text-base font-semibold mt-3 mb-1.5 first:mt-0" style={{ color: 'hsl(var(--text-high))' }}>
              {children}
            </h3>
          ),
          h4: ({ children }) => (
            <h4 className="text-sm font-semibold mt-3 mb-1 first:mt-0" style={{ color: 'hsl(var(--text-high))' }}>
              {children}
            </h4>
          ),
          p: ({ children }) => <p className="my-2 first:mt-0 last:mb-0">{children}</p>,
          ul: ({ children }) => <ul className="list-disc pl-5 my-2 space-y-1">{children}</ul>,
          ol: ({ children }) => <ol className="list-decimal pl-5 my-2 space-y-1">{children}</ol>,
          li: ({ children }) => <li className="my-0.5">{children}</li>,
          a: ({ href, children }) => (
            <a
              href={href}
              target="_blank"
              rel="noopener noreferrer"
              className="underline underline-offset-2 hover:no-underline"
              style={{ color: 'hsl(var(--brand))' }}
            >
              {children}
            </a>
          ),
          // Inline `code` and fenced ``` are both routed through this
          // `code` callback; we tell them apart by checking for a
          // newline or a `language-…` class. Block-form gets pre-wrap
          // so long lines wrap inside the bordered <pre> below.
          code: ({ className: cls, children }) => {
            const lang = /language-(\w+)/.exec(cls || '')?.[1]
            const txt = String(children ?? '').replace(/\n$/, '')
            const isBlock = txt.includes('\n') || !!lang
            if (isBlock) {
              return (
                <code
                  className="block whitespace-pre overflow-x-auto font-mono text-[12px]"
                  style={{ color: 'hsl(var(--text-high))' }}
                >
                  {txt}
                </code>
              )
            }
            return (
              <code
                className="px-1 py-0.5 rounded font-mono text-[12px]"
                style={{
                  background: 'hsl(var(--bg-secondary))',
                  color: 'hsl(var(--text-high))',
                  border: '1px solid hsl(var(--border))',
                }}
              >
                {txt}
              </code>
            )
          },
          pre: ({ children }) => (
            <pre
              className="my-2 p-3 rounded-md overflow-x-auto text-[12px]"
              style={{
                background: 'hsl(var(--bg-panel))',
                border: '1px solid hsl(var(--border))',
                color: 'hsl(var(--text-high))',
              }}
            >
              {children}
            </pre>
          ),
          blockquote: ({ children }) => (
            <blockquote
              className="pl-3 my-2 italic"
              style={{
                borderLeft: '2px solid hsl(var(--border))',
                color: 'hsl(var(--text-low))',
              }}
            >
              {children}
            </blockquote>
          ),
          hr: () => <hr className="my-3" style={{ borderColor: 'hsl(var(--border))' }} />,
          table: ({ children }) => (
            <div className="my-2 overflow-x-auto">
              <table
                className="border-collapse text-[13px]"
                style={{ color: 'hsl(var(--text-normal))' }}
              >
                {children}
              </table>
            </div>
          ),
          th: ({ children }) => (
            <th
              className="px-2 py-1 text-left font-semibold"
              style={{
                background: 'hsl(var(--bg-secondary))',
                border: '1px solid hsl(var(--border))',
                color: 'hsl(var(--text-high))',
              }}
            >
              {children}
            </th>
          ),
          td: ({ children }) => (
            <td
              className="px-2 py-1 align-top"
              style={{ border: '1px solid hsl(var(--border))' }}
            >
              {children}
            </td>
          ),
          strong: ({ children }) => (
            <strong className="font-semibold" style={{ color: 'hsl(var(--text-high))' }}>
              {children}
            </strong>
          ),
          em: ({ children }) => <em className="italic">{children}</em>,
        }}
      >
        {children}
      </ReactMarkdown>
    </div>
  )
}
