// The rendered preview of an .html file in the Files viewer. The file is
// untrusted (an agent wrote it, or it came off the internet), so it is shown in
// an iframe whose sandbox omits allow-same-origin: its scripts run, but in an
// opaque origin with no access to lasso's DOM, storage or cookies. Never add
// allow-same-origin here — together with allow-scripts it lets the document
// remove its own sandbox.
export const HTML_PREVIEW_SANDBOX =
  "allow-scripts allow-popups allow-modals allow-downloads"

// An opaque origin still sends requests, and lasso's file endpoints have no
// CSRF guard: on an open tailnet lasso a script could POST /api/file. So the
// document also gets a CSP that forbids fetch/XHR/WebSocket/beacons and form
// submission outright, and nested frames (which would carry no CSP of ours).
// Inline and https: scripts, styles, fonts and images stay allowed, since a
// self-contained report (inline JS, a Google Fonts link) is the common case.
// A CSP set by <meta> can be tightened by the document but never relaxed.
export const HTML_PREVIEW_CSP = [
  "default-src 'none'",
  "script-src 'unsafe-inline' 'unsafe-eval' https: data: blob:",
  "style-src 'unsafe-inline' https: data:",
  "img-src https: data: blob:",
  "font-src https: data:",
  "media-src https: data: blob:",
  "worker-src blob: data:",
  "connect-src 'none'",
  "form-action 'none'",
  "frame-src 'none'",
].join("; ")

const escapeAttr = (s: string) =>
  s.replace(/&/g, "&amp;").replace(/"/g, "&quot;")

// The head lasso puts in front of the file. <base href="about:srcdoc"> does two
// jobs: a srcdoc document otherwise resolves relative URLs against LASSO's page
// URL, so `href="#section"` navigated the frame to lasso itself and `img.png`
// requested lasso's own origin. Against about:srcdoc a fragment stays in the
// document, and a relative path fails to resolve and is never fetched. Sibling
// files (css/img next to the .html) are therefore not loaded; the frame has no
// credentials to fetch them through lasso anyway.
const PREAMBLE =
  `<meta http-equiv="Content-Security-Policy" content="${escapeAttr(HTML_PREVIEW_CSP)}">` +
  `<base href="about:srcdoc">`

// Leading BOM, whitespace and comments may precede the doctype.
const DOCTYPE_RE = /^(\u{feff}?(?:\s|<!--[\s\S]*?-->)*<!doctype[^>]*>)/iu

// The srcdoc for a file's source: the preamble inserted right after the doctype.
// A <meta> CSP only counts inside <head>; placed before any other tag, the
// parser opens an implicit head for it and the file's own <html>/<head> merge
// into that. It must come AFTER the doctype, or the page drops to quirks mode.
export function htmlPreviewDoc(source: string): string {
  const m = DOCTYPE_RE.exec(source)
  if (!m) return PREAMBLE + source
  return m[1] + PREAMBLE + source.slice(m[1].length)
}
