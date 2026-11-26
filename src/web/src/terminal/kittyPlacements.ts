// Placement identity for xterm's image addon.
//
// The kitty graphics protocol separates an image (its data, id `i`) from its
// placements (where it is drawn, id `p`): one image can be placed many times,
// placing `(i, p)` again moves that one placement, and `a=d,d=i,i=…,p=…`
// removes that one placement and keeps the image. @xterm/addon-image keeps one
// record per image and none per placement (xtermjs/xterm.js#5710, #6132), so:
//
//   - deleting one placement frees the whole image, and every later placement
//     of it finds no image and draws nothing;
//   - placing `(i, p)` again decodes and draws again, leaving the old tiles.
//
// herdr draws an image a program placed with Unicode placeholders as one
// placement per row of cells, re-sends them all with each frame, and deletes
// each by `(i, p)` when the rows move. Without this file such an image shows
// once, then sticks where it first was and loses rows the moment anything
// scrolls, and the re-sent placements cost a decode each.
//
// This keeps the missing record, from outside the addon: which storage entry
// each `(i, p)` drew, and where. It wraps three private methods of the addon's
// kitty handler, so it checks that they are there and does nothing when they
// are not: a build of the addon that tracks placements itself has no use for
// it, and one that merely renamed things must not break the terminal.

/** The fields of the addon's parsed kitty command that are read here. */
export interface KittyCommand {
  id?: number
  placementId?: number
  deleteSelector?: string
  columns?: number
  rows?: number
  x?: number
  y?: number
  width?: number
  height?: number
  zIndex?: number
}

interface ImageSpec {
  tileCount: number
}

/** The storage every image protocol shares: one entry per drawn picture. */
interface SharedStorage {
  deleteImage(id: number): void
  _images: Map<number, ImageSpec>
}

interface KittyStorage {
  addImage(kittyId: number, ...rest: unknown[]): void
  _kittyIdToStorageId: Map<number, number>
  _storageIdToKittyId: Map<number, number>
  _storage: SharedStorage
}

export interface KittyHandler {
  _handleDelete(cmd: KittyCommand): boolean
  _displayImage(image: { id: number }, cmd: KittyCommand): Promise<boolean>
  _kittyStorage: KittyStorage
}

/** What one placement last drew. */
interface Drawn {
  storageId: number
  /** Where and how it was drawn; the same again is the same placement. */
  signature: string
  /** How many cells it covered, to tell when text has since overwritten some. */
  tiles: number
}

const isMap = (v: unknown): v is Map<unknown, unknown> => v instanceof Map
const isFn = (v: unknown): v is (...args: never[]) => unknown =>
  typeof v === "function"

/** The addon's kitty handler, when it has the shape this file was written for. */
export function kittyHandlerOf(addon: unknown): KittyHandler | undefined {
  const handlers = (addon as { _handlers?: unknown } | undefined)?._handlers
  if (!isMap(handlers)) return undefined
  const h = handlers.get("kitty") as Partial<KittyHandler> | undefined
  const ks = h?._kittyStorage as Partial<KittyStorage> | undefined
  const shared = ks?._storage as Partial<SharedStorage> | undefined
  if (
    !h ||
    !ks ||
    !shared ||
    !isFn(h._handleDelete) ||
    !isFn(h._displayImage) ||
    !isFn(ks.addImage) ||
    !isMap(ks._kittyIdToStorageId) ||
    !isMap(ks._storageIdToKittyId) ||
    !isFn(shared.deleteImage) ||
    !isMap(shared._images)
  ) {
    return undefined
  }
  return h as KittyHandler
}

/**
 * Gives the handler's placements an identity. `cursor` names the cell the next
 * placement will be drawn at.
 */
export function trackKittyPlacements(
  handler: KittyHandler,
  cursor: () => string
): void {
  const ks = handler._kittyStorage
  const shared = ks._storage
  /** image id -> placement id -> what it drew */
  const drawn = new Map<number, Map<number, Drawn>>()
  /** The placement being displayed, between `_displayImage` and `addImage`. */
  let current: { placementId: number; signature: string } | undefined

  // The addon frees an image's data when the storage entry it last drew goes,
  // so an entry is unhooked from its image before it is removed.
  const remove = (imageId: number, storageId: number) => {
    ks._storageIdToKittyId.delete(storageId)
    if (ks._kittyIdToStorageId.get(imageId) === storageId) {
      ks._kittyIdToStorageId.delete(imageId)
    }
    shared.deleteImage(storageId)
  }
  const removeAll = (imageId: number) => {
    for (const one of drawn.get(imageId)?.values() ?? []) {
      remove(imageId, one.storageId)
    }
    drawn.delete(imageId)
  }

  const displayImage = handler._displayImage.bind(handler)
  handler._displayImage = (image, cmd) => {
    current = undefined
    const placementId = cmd.placementId
    if (!placementId) return displayImage(image, cmd) // anonymous: always a new one
    const signature = [
      cursor(),
      cmd.columns,
      cmd.rows,
      cmd.x,
      cmd.y,
      cmd.width,
      cmd.height,
      cmd.zIndex,
    ].join(",")
    const last = drawn.get(image.id)?.get(placementId)
    if (
      last &&
      last.signature === signature &&
      shared._images.get(last.storageId)?.tileCount === last.tiles
    ) {
      // Already there, in the same cells, drawn the same way, with no cell
      // overwritten since: drawing it again would change nothing.
      return Promise.resolve(true)
    }
    current = { placementId, signature }
    return displayImage(image, cmd).finally(() => {
      current = undefined
    })
  }

  const addImage = ks.addImage.bind(ks)
  ks.addImage = (kittyId, ...rest) => {
    const placing = current
    current = undefined
    addImage(kittyId, ...rest)
    if (!placing) return
    const storageId = ks._kittyIdToStorageId.get(kittyId)
    if (storageId === undefined) return
    let byPlacement = drawn.get(kittyId)
    if (!byPlacement) {
      byPlacement = new Map()
      drawn.set(kittyId, byPlacement)
    }
    // The same placement drawn somewhere else replaces what it drew before.
    const last = byPlacement.get(placing.placementId)
    if (last && last.storageId !== storageId) remove(kittyId, last.storageId)
    // addImage has just mapped the image to the new entry; put that back after
    // remove() in case the old entry was the mapped one.
    ks._kittyIdToStorageId.set(kittyId, storageId)
    ks._storageIdToKittyId.set(storageId, kittyId)
    byPlacement.set(placing.placementId, {
      storageId,
      signature: placing.signature,
      tiles: shared._images.get(storageId)?.tileCount ?? 0,
    })
  }

  const handleDelete = handler._handleDelete.bind(handler)
  handler._handleDelete = (cmd) => {
    const selector = cmd.deleteSelector ?? "a"
    const byImage = selector === "i" || selector === "I"
    if (byImage && cmd.id !== undefined && cmd.placementId) {
      // One placement: what it drew goes, the image stays. The capital frees
      // the image too, once this was its last placement.
      const byPlacement = drawn.get(cmd.id)
      const last = byPlacement?.get(cmd.placementId)
      if (last) {
        remove(cmd.id, last.storageId)
        byPlacement?.delete(cmd.placementId)
        if (byPlacement?.size === 0) drawn.delete(cmd.id)
      }
      if (selector === "I" && !drawn.has(cmd.id)) return handleDelete(cmd)
      return true
    }
    // The whole image, or everything: every placement this file knows of goes
    // with it, where the addon alone would remove only the last one drawn.
    if (byImage && cmd.id !== undefined) {
      removeAll(cmd.id)
    } else if (selector === "a" || selector === "A") {
      for (const imageId of [...drawn.keys()]) removeAll(imageId)
    }
    return handleDelete(cmd)
  }
}

/** The cell the terminal's cursor is on, counted from the top of scrollback. */
interface CursorSource {
  buffer: { active: { baseY: number; cursorY: number; cursorX: number } }
}

/**
 * Turns placement tracking on for a loaded image addon. Returns whether it
 * did: false means the addon is not the shape this was written against.
 */
export function installKittyPlacementTracking(
  addon: unknown,
  terminal: CursorSource
): boolean {
  try {
    const handler = kittyHandlerOf(addon)
    if (!handler) return false
    trackKittyPlacements(handler, () => {
      const b = terminal.buffer.active
      return `${b.baseY + b.cursorY}:${b.cursorX}`
    })
    return true
  } catch {
    return false
  }
}
