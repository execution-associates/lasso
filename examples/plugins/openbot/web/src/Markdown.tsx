import type { Components } from "react-markdown"
import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"

// The agent's prose, rendered. Raw HTML in it is NOT rendered (no rehype-raw):
// the text is model output, often quoting email, so markup stays text.
//
// Images are shown as links rather than loaded. Jessica quotes mail and web
// pages, and an image URL in them is someone else's tracking pixel; the page's
// own origin is opaque, but the request would still tell that server the
// message was read. Links open in a new window (the frame allows popups) and
// carry no referrer or opener.
const components: Components = {
  a: ({ href, children }) => (
    <a href={href} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  ),
  img: ({ src, alt }) =>
    typeof src === "string" && src ? (
      <a href={src} target="_blank" rel="noopener noreferrer">
        [image{alt ? `: ${alt}` : ""}]
      </a>
    ) : null,
}

export function Markdown({ source }: { source: string }) {
  return (
    <div className="md">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {source}
      </ReactMarkdown>
    </div>
  )
}
