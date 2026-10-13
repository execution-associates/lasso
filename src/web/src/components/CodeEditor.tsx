import { EditorView } from "@codemirror/view"
import CodeMirror from "@uiw/react-codemirror"
import * as React from "react"
import {
  changedLinesHighlight,
  editorTheme,
  languageExtension,
} from "@/lib/codemirror"

// Above this size we skip the language extension (and its parsing cost) but
// still open the file in the editor.
export const HILITE_CAP = 400 * 1024

// A CodeMirror 6 editor themed to the live herdr palette (see lib/codemirror).
// basicSetup gives line numbers, the fold gutter, bracket matching and in-editor
// search (⌘/Ctrl+F). For very large files we drop the language extension to skip
// the parsing cost — editing still works, just without highlighting.
//
// Its own module so the Mac app's file window (desktop/file-window.tsx) can
// open the same editor without pulling in the Files viewer and the app store
// it reads.
export function CodeEditor({
  value,
  path,
  onChange,
  changedLines,
  line,
  lineSeq,
  readOnly = false,
  onView,
}: {
  value: string
  path: string
  onChange: (v: string) => void
  changedLines: number[]
  line: number | null
  lineSeq: number
  readOnly?: boolean
  // The live EditorView, for a host that has to read or keep its scroll
  // position across a buffer swap.
  onView?: (view: EditorView | null) => void
}) {
  // Recompute only when the file, the large-file threshold, or the changed-line
  // set changes — not on every keystroke — so CodeMirror isn't reconfigured as
  // the user types.
  const big = value.length > HILITE_CAP
  const extensions = React.useMemo(() => {
    const lang = big ? null : languageExtension(path)
    return [
      editorTheme,
      EditorView.lineWrapping,
      ...(lang ? [lang] : []),
      ...(changedLines.length ? [changedLinesHighlight(changedLines)] : []),
    ]
  }, [path, big, changedLines])

  // Scroll a requested line into the middle of the view and put the cursor on
  // it, which the active-line band highlights. Not focused: on a phone that
  // would pop the keyboard over a file the human only asked to look at. The
  // view arrives through onCreateEditor, so this also runs once the editor
  // exists when the line was asked for before the text had loaded.
  const [view, setView] = React.useState<EditorView | null>(null)
  // biome-ignore lint/correctness/useExhaustiveDependencies: lineSeq is the re-request trigger
  React.useEffect(() => {
    if (!view || !line) return
    const doc = view.state.doc
    const at = doc.line(Math.min(Math.max(1, line), doc.lines))
    view.dispatch({
      selection: { anchor: at.from },
      effects: EditorView.scrollIntoView(at.from, { y: "center" }),
    })
  }, [view, line, lineSeq])

  React.useEffect(() => {
    if (!onView) return
    onView(view)
    return () => onView(null)
  }, [view, onView])

  return (
    <CodeMirror
      value={value}
      onChange={onChange}
      theme="none"
      // Use the browser's native selection (styled in lib/codemirror) instead of
      // CodeMirror's drawn one — the drawn band can't recolor selected text and
      // read as nearly invisible on light themes.
      basicSetup={{ drawSelection: false }}
      extensions={extensions}
      readOnly={readOnly}
      height="100%"
      className="cm-host"
      onCreateEditor={setView}
    />
  )
}
