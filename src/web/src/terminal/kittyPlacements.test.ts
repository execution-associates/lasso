import { describe, expect, test } from "bun:test"
import {
  installKittyPlacementTracking,
  type KittyCommand,
  kittyHandlerOf,
} from "./kittyPlacements"

// A stand-in for @xterm/addon-image's kitty handler with the behaviour the
// tracking exists to correct: one record per image, none per placement, and
// an image freed as soon as the entry it last drew is deleted.
function fakeAddon() {
  let nextStorageId = 1
  const sharedImages = new Map<number, { tileCount: number }>()
  const imageData = new Set<number>()
  const kittyIdToStorageId = new Map<number, number>()
  const storageIdToKittyId = new Map<number, number>()
  const shared = {
    _images: sharedImages,
    deleteImage(id: number) {
      sharedImages.delete(id)
      const kittyId = storageIdToKittyId.get(id)
      if (kittyId !== undefined) {
        // the addon's onImageDeleted hook: the image goes with its entry
        imageData.delete(kittyId)
        kittyIdToStorageId.delete(kittyId)
        storageIdToKittyId.delete(id)
      }
    },
  }
  const kittyStorage = {
    _storage: shared,
    _kittyIdToStorageId: kittyIdToStorageId,
    _storageIdToKittyId: storageIdToKittyId,
    addImage(kittyId: number) {
      const old = kittyIdToStorageId.get(kittyId)
      if (old !== undefined) storageIdToKittyId.delete(old)
      const id = nextStorageId++
      sharedImages.set(id, { tileCount: 60 })
      kittyIdToStorageId.set(kittyId, id)
      storageIdToKittyId.set(id, kittyId)
    },
    deleteById(kittyId: number) {
      imageData.delete(kittyId)
      const id = kittyIdToStorageId.get(kittyId)
      if (id !== undefined) shared.deleteImage(id)
    },
  }
  const stats = { decodes: 0, misses: 0 }
  const handler = {
    _kittyStorage: kittyStorage,
    async _displayImage(image: { id: number }, _cmd: KittyCommand) {
      stats.decodes++
      kittyStorage.addImage(image.id)
      return true
    },
    _handleDelete(cmd: KittyCommand) {
      if (cmd.id !== undefined) kittyStorage.deleteById(cmd.id)
      return true
    },
  }
  const addon = { _handlers: new Map<string, unknown>([["kitty", handler]]) }
  const cursor = { baseY: 0, cursorY: 0, cursorX: 0 }
  const terminal = { buffer: { active: cursor } }
  return {
    addon,
    terminal,
    stats,
    sharedImages,
    transmit: (id: number) => imageData.add(id),
    hasImage: (id: number) => imageData.has(id),
    /** `a=p` at a row, as the addon's parser would dispatch it. */
    async place(id: number, placementId: number, row: number) {
      if (!imageData.has(id)) {
        stats.misses++
        return
      }
      cursor.cursorY = row
      await handler._displayImage(
        { id },
        { id, placementId, columns: 60, rows: 1 }
      )
    },
    remove: (cmd: KittyCommand) => handler._handleDelete(cmd),
  }
}

describe("kitty placement tracking", () => {
  test("the fake shows the fault: deleting one placement loses the image", async () => {
    const t = fakeAddon()
    t.transmit(7)
    await t.place(7, 1, 10)
    await t.place(7, 2, 11)
    t.remove({ id: 7, placementId: 1, deleteSelector: "i" })
    expect(t.hasImage(7)).toBe(false)
    await t.place(7, 3, 4)
    expect(t.stats.misses).toBe(1)
  })

  test("deleting one placement removes what it drew and keeps the image", async () => {
    const t = fakeAddon()
    expect(installKittyPlacementTracking(t.addon, t.terminal)).toBe(true)
    t.transmit(7)
    await t.place(7, 1, 10)
    await t.place(7, 2, 11)
    expect(t.sharedImages.size).toBe(2)

    t.remove({ id: 7, placementId: 1, deleteSelector: "i" })
    expect(t.hasImage(7)).toBe(true)
    expect(t.sharedImages.size).toBe(1)

    // the rows moved: old placements deleted, new ones placed, as herdr does
    t.remove({ id: 7, placementId: 2, deleteSelector: "i" })
    await t.place(7, 3, 4)
    await t.place(7, 4, 5)
    expect(t.stats.misses).toBe(0)
    expect(t.sharedImages.size).toBe(2)
  })

  test("a placement sent again unchanged is not drawn again", async () => {
    const t = fakeAddon()
    installKittyPlacementTracking(t.addon, t.terminal)
    t.transmit(7)
    await t.place(7, 1, 10)
    for (let i = 0; i < 50; i++) await t.place(7, 1, 10)
    expect(t.stats.decodes).toBe(1)
    expect(t.sharedImages.size).toBe(1)
  })

  test("a placement sent again after text overwrote a cell is drawn again", async () => {
    const t = fakeAddon()
    installKittyPlacementTracking(t.addon, t.terminal)
    t.transmit(7)
    await t.place(7, 1, 10)
    const [entry] = [...t.sharedImages.values()]
    if (entry) entry.tileCount--
    await t.place(7, 1, 10)
    expect(t.stats.decodes).toBe(2)
    expect(t.sharedImages.size).toBe(1)
  })

  test("the same placement at another cell replaces what it drew", async () => {
    const t = fakeAddon()
    installKittyPlacementTracking(t.addon, t.terminal)
    t.transmit(7)
    await t.place(7, 1, 10)
    await t.place(7, 1, 20)
    expect(t.stats.decodes).toBe(2)
    expect(t.sharedImages.size).toBe(1)
    expect(t.hasImage(7)).toBe(true)
  })

  test("deleting the image removes every placement of it", async () => {
    const t = fakeAddon()
    installKittyPlacementTracking(t.addon, t.terminal)
    t.transmit(7)
    await t.place(7, 1, 10)
    await t.place(7, 2, 11)
    await t.place(7, 3, 12)
    t.remove({ id: 7, deleteSelector: "I" })
    expect(t.sharedImages.size).toBe(0)
    expect(t.hasImage(7)).toBe(false)
  })

  test("the capital selector frees the image with its last placement", async () => {
    const t = fakeAddon()
    installKittyPlacementTracking(t.addon, t.terminal)
    t.transmit(7)
    await t.place(7, 1, 10)
    await t.place(7, 2, 11)
    t.remove({ id: 7, placementId: 1, deleteSelector: "I" })
    expect(t.hasImage(7)).toBe(true)
    t.remove({ id: 7, placementId: 2, deleteSelector: "I" })
    expect(t.hasImage(7)).toBe(false)
    expect(t.sharedImages.size).toBe(0)
  })

  test("an addon of another shape is left alone", () => {
    expect(kittyHandlerOf({})).toBeUndefined()
    expect(
      kittyHandlerOf({ _handlers: new Map([["kitty", {}]]) })
    ).toBeUndefined()
    expect(
      installKittyPlacementTracking(undefined, {
        buffer: { active: { baseY: 0, cursorY: 0, cursorX: 0 } },
      })
    ).toBe(false)
  })
})
