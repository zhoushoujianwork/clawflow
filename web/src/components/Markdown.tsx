import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { cn } from '../lib/utils'

interface MarkdownProps {
  children: string
  className?: string
}

// Markdown wraps react-markdown with GFM (tables, task lists, strikethrough,
// fenced code) and a Tailwind class layer that matches the run-detail UI.
// We deliberately don't pull in @tailwindcss/typography here — the run pages
// use a denser, monospace-leaning style than `prose` provides, and the
// hand-rolled overrides below stay in sync with the rest of the file's
// look (rounded code blocks, thin borders, muted heading color).
export function Markdown({ children, className }: MarkdownProps) {
  return (
    <div className={cn('text-sm leading-relaxed text-foreground', className)}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          h1: ({ children }) => <h1 className="text-xl font-bold mt-4 mb-2 first:mt-0">{children}</h1>,
          h2: ({ children }) => <h2 className="text-lg font-semibold mt-4 mb-2 first:mt-0">{children}</h2>,
          h3: ({ children }) => <h3 className="text-base font-semibold mt-3 mb-1.5 first:mt-0">{children}</h3>,
          h4: ({ children }) => <h4 className="text-sm font-semibold mt-3 mb-1 first:mt-0">{children}</h4>,
          p: ({ children }) => <p className="my-2 first:mt-0 last:mb-0">{children}</p>,
          ul: ({ children }) => <ul className="list-disc pl-5 my-2 space-y-1">{children}</ul>,
          ol: ({ children }) => <ol className="list-decimal pl-5 my-2 space-y-1">{children}</ol>,
          li: ({ children }) => <li className="my-0.5">{children}</li>,
          a: ({ href, children }) => (
            <a
              href={href}
              target="_blank"
              rel="noopener noreferrer"
              className="text-primary underline underline-offset-2 hover:no-underline"
            >
              {children}
            </a>
          ),
          // Distinguish inline `code` from fenced ```code blocks```.
          // ReactMarkdown passes `inline` via parents but we infer it
          // from the absence of \n and a parent <pre>. Easier: render
          // <code> the same minimal way; <pre> wraps fenced blocks.
          code: ({ className: cls, children }) => {
            const lang = /language-(\w+)/.exec(cls || '')?.[1]
            const txt = String(children ?? '').replace(/\n$/, '')
            const isBlock = txt.includes('\n') || !!lang
            if (isBlock) {
              return (
                <code className="block whitespace-pre overflow-x-auto font-mono text-[12px]">
                  {txt}
                </code>
              )
            }
            return (
              <code className="px-1 py-0.5 rounded bg-secondary text-[12px] font-mono">
                {txt}
              </code>
            )
          },
          pre: ({ children }) => (
            <pre className="my-2 p-3 rounded-md bg-secondary/60 border border-border overflow-x-auto text-[12px]">
              {children}
            </pre>
          ),
          blockquote: ({ children }) => (
            <blockquote className="border-l-2 border-border pl-3 my-2 text-muted-foreground italic">
              {children}
            </blockquote>
          ),
          hr: () => <hr className="my-3 border-border" />,
          table: ({ children }) => (
            <div className="my-2 overflow-x-auto">
              <table className="border-collapse text-[13px]">{children}</table>
            </div>
          ),
          th: ({ children }) => (
            <th className="border border-border px-2 py-1 bg-secondary/60 text-left font-semibold">
              {children}
            </th>
          ),
          td: ({ children }) => <td className="border border-border px-2 py-1 align-top">{children}</td>,
          strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
          em: ({ children }) => <em className="italic">{children}</em>,
        }}
      >
        {children}
      </ReactMarkdown>
    </div>
  )
}
