// The Mac app's file window (desktop/file-window.tsx) as a state machine: one
// document's buffer against the disk it came from. Pure, so the rules that
// decide whether a change on disk may replace what someone typed are tested
// without a page (file-doc.test.js).
//
// The one rule everything here serves: a changed-on-disk event never replaces
// a dirty buffer. A clean buffer follows the disk silently; a dirty one keeps
// the person's edits and raises `conflict`, which they answer with takeDisk
// (drop mine) or keepMine (overwrite on purpose).
//
// `text`/`mtime` are the BASE: the disk content the buffer started from and
// the mtime it was read at. A save sends `mtime` as base_mtime, so the server
// refuses (409) a write over a version this buffer never saw — the gap between
// the native watcher's last poll and the save.

export type FileMode = "rich" | "raw"

export interface FileDoc {
  // The first read has landed (or the file was found missing).
  loaded: boolean
  // The base: what the disk held when the buffer last agreed with it.
  text: string | null
  mtime: number | null
  // The buffer. Dirty exactly when it differs from `text`.
  draft: string | null
  // The file is gone from disk. The buffer is kept: a save puts it back.
  missing: boolean
  // A read that failed for any reason but the file being absent.
  loadError: string | null
  // The disk moved under a dirty buffer, or a save was refused for it.
  conflict: boolean
  // The disk's version while in conflict, so keepMine can adopt its mtime as
  // the new base. `text` is null when only a 409 has been seen so far.
  disk: { text: string | null; mtime: number | null } | null
  saving: boolean
  saveError: string | null
  mode: FileMode
}

export type FileDocAction =
  // A new file (or the same one re-targeted) starts loading.
  | { type: "reset"; mode: FileMode }
  // The first read, and takeDisk's: the buffer becomes the disk.
  | { type: "loaded"; text: string; mtime: number | null }
  | { type: "load-failed"; error: string; missing: boolean }
  // changed(): a fresh read of the disk.
  | { type: "disk"; text: string; mtime: number | null }
  // changed() found the file gone.
  | { type: "disk-missing" }
  | { type: "edit"; draft: string }
  | { type: "mode"; mode: FileMode }
  | { type: "save-start" }
  // `content` is what was written: the buffer may have moved on since.
  | { type: "save-ok"; content: string; mtime: number | null }
  | { type: "save-conflict"; mtime: number | null }
  | { type: "save-failed"; error: string }
  | { type: "keep-mine" }

export const CONFLICT_SAVE_ERROR = "Changed on disk"

export function initialFileDoc(mode: FileMode): FileDoc {
  return {
    loaded: false,
    text: null,
    mtime: null,
    draft: null,
    missing: false,
    loadError: null,
    conflict: false,
    disk: null,
    saving: false,
    saveError: null,
    mode,
  }
}

export function isDirty(s: FileDoc): boolean {
  return s.draft != null && s.draft !== s.text
}

export function fileDocReducer(s: FileDoc, a: FileDocAction): FileDoc {
  switch (a.type) {
    case "reset":
      return initialFileDoc(a.mode)

    case "loaded":
      return {
        ...s,
        loaded: true,
        text: a.text,
        mtime: a.mtime,
        draft: a.text,
        missing: false,
        loadError: null,
        conflict: false,
        disk: null,
        saveError: null,
      }

    case "load-failed":
      return {
        ...s,
        loaded: true,
        missing: a.missing,
        loadError: a.missing ? null : a.error,
      }

    case "disk": {
      // A save in flight is about to move the base itself, and its own write
      // would read as someone else's change; the page looks again once it
      // settles.
      if (s.saving) return s
      // Nothing to protect yet: the disk simply is the document.
      if (s.draft == null) return fileDocReducer(s, { ...a, type: "loaded" })
      const base = { ...s, missing: false, loadError: null }
      if (!isDirty(s)) {
        // A clean buffer follows the disk. Same content is not a change, but
        // the mtime still moves so the next save's base is the current one.
        if (a.text === s.text) return { ...base, mtime: a.mtime }
        return {
          ...base,
          text: a.text,
          mtime: a.mtime,
          draft: a.text,
          conflict: false,
          disk: null,
        }
      }
      // Dirty from here on: the buffer is never touched.
      if (a.text === s.draft) {
        // The disk now holds exactly what was typed (this window's own save
        // seen by the watcher, or the same edit made elsewhere): nothing is
        // lost, and the buffer is clean against it.
        return {
          ...base,
          text: a.text,
          mtime: a.mtime,
          conflict: false,
          disk: null,
          saveError: null,
        }
      }
      if (a.text === s.text) {
        // Touched but not changed (a `touch`, a rewrite of the same bytes), or
        // changed and put back: the edits are still based on what the disk
        // holds, so the base mtime follows and any conflict is over.
        return {
          ...base,
          mtime: a.mtime,
          conflict: false,
          disk: null,
          saveError: s.saveError === CONFLICT_SAVE_ERROR ? null : s.saveError,
        }
      }
      return { ...base, conflict: true, disk: { text: a.text, mtime: a.mtime } }
    }

    case "disk-missing":
      return { ...s, loaded: true, missing: true }

    case "edit":
      if (s.draft == null) return s
      return { ...s, draft: a.draft }

    case "mode":
      return { ...s, mode: a.mode }

    case "save-start":
      return { ...s, saving: true, saveError: null }

    case "save-ok":
      return {
        ...s,
        saving: false,
        saveError: null,
        text: a.content,
        mtime: a.mtime,
        missing: false,
        conflict: false,
        disk: null,
      }

    case "save-conflict":
      return {
        ...s,
        saving: false,
        saveError: CONFLICT_SAVE_ERROR,
        conflict: true,
        disk: {
          // A 409 says only when the disk moved, not what it holds; keep any
          // content already read for this conflict.
          text: s.disk?.text ?? null,
          mtime: a.mtime,
        },
      }

    case "save-failed":
      return { ...s, saving: false, saveError: a.error }

    case "keep-mine": {
      if (!s.conflict) return s
      // Overwriting on purpose: the disk's version becomes the base, so the
      // next save's base_mtime matches and is accepted, and a later look at
      // that same disk content is not a new conflict.
      const disk = s.disk
      return {
        ...s,
        conflict: false,
        saveError: null,
        disk: null,
        text: disk?.text ?? s.text,
        mtime: disk ? disk.mtime : s.mtime,
      }
    }
  }
}

// What the page reports to the native window ({op: "file-state", ...}).
export interface FileStateReport {
  dirty: boolean
  mode: FileMode
  saving: boolean
  saveError: string | null
  conflict: boolean
  loaded: boolean
  missing: boolean
  mtime: number | null
}

export function fileStateReport(s: FileDoc): FileStateReport {
  return {
    dirty: isDirty(s),
    mode: s.mode,
    saving: s.saving,
    saveError: s.saveError,
    conflict: s.conflict,
    loaded: s.loaded,
    missing: s.missing,
    mtime: s.mtime,
  }
}

// Two reports differ (so the page posts a new one).
export function sameReport(a: FileStateReport, b: FileStateReport): boolean {
  return (
    a.dirty === b.dirty &&
    a.mode === b.mode &&
    a.saving === b.saving &&
    a.saveError === b.saveError &&
    a.conflict === b.conflict &&
    a.loaded === b.loaded &&
    a.missing === b.missing &&
    a.mtime === b.mtime
  )
}
