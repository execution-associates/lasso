import { FileText } from "lucide-react"

import { api, type ChatFile, type ChatItem } from "@/lib/api"

// Files an agent showed the human (Claude Code's SendUserFile, lasso's
// tool.files): an image inline, audio and video as players, anything else a
// chip that opens it. The bytes come through /api/file from the host that
// served the transcript, with download=1 so a big file skips the preview cap.

export function sharedFiles(items: ChatItem[] | undefined): ChatFile[] {
  const out: ChatFile[] = []
  for (const it of items ?? [])
    for (const f of it.tool?.files ?? []) out.push(f)
  return out
}

function baseName(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1) || path
}

export function SharedFiles({
  files,
  host,
}: {
  files: ChatFile[]
  host: string
}) {
  if (files.length === 0) return null
  return (
    <div className="flex flex-col gap-2.5">
      {files.map((f) => {
        const url = api.downloadURL(f.path, host)
        return (
          <figure key={f.path} className="flex flex-col gap-1">
            {f.kind === "image" ? (
              <a href={url} target="_blank" rel="noreferrer">
                <img
                  src={url}
                  alt={f.caption || baseName(f.path)}
                  loading="lazy"
                  className="max-h-[60vh] w-full rounded-2xl bg-muted object-contain"
                />
              </a>
            ) : f.kind === "audio" ? (
              // biome-ignore lint/a11y/useMediaCaption: an agent's file carries no captions to offer.
              <audio src={url} controls preload="none" className="w-full" />
            ) : f.kind === "video" ? (
              // biome-ignore lint/a11y/useMediaCaption: an agent's file carries no captions to offer.
              <video
                src={url}
                controls
                playsInline
                preload="metadata"
                className="max-h-[60vh] w-full rounded-2xl bg-black"
              />
            ) : (
              <a
                href={url}
                target="_blank"
                rel="noreferrer"
                className="flex items-center gap-2.5 rounded-2xl border border-border bg-background/40 px-3.5 py-3 text-[14px] text-foreground"
              >
                <FileText className="size-4 shrink-0 text-muted-foreground" />
                <span className="min-w-0 truncate">{baseName(f.path)}</span>
              </a>
            )}
            {f.caption && (
              <figcaption className="text-[12.5px] text-muted-foreground">
                {f.caption}
              </figcaption>
            )}
          </figure>
        )
      })}
    </div>
  )
}
