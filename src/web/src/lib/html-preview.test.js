import { describe, expect, test } from "bun:test"

import {
  HTML_PREVIEW_CSP,
  HTML_PREVIEW_SANDBOX,
  htmlPreviewDoc,
} from "@/lib/html-preview"

describe("htmlPreviewDoc", () => {
  test("puts the preamble after the doctype, so the page stays in standards mode", () => {
    const doc = htmlPreviewDoc("<!DOCTYPE html>\n<html><head></head></html>")
    expect(doc.startsWith("<!DOCTYPE html><meta http-equiv")).toBe(true)
    expect(doc).toContain('<base href="about:srcdoc">')
  })

  test("keeps a BOM and leading comments ahead of the doctype", () => {
    const bom = String.fromCharCode(0xfeff)
    const src = `${bom}<!-- hi -->\n<!doctype html><p>x</p>`
    expect(
      htmlPreviewDoc(src).startsWith(`${bom}<!-- hi -->\n<!doctype html><meta`)
    ).toBe(true)
  })

  test("prepends the preamble to a fragment with no doctype", () => {
    expect(htmlPreviewDoc("<p>x</p>").startsWith("<meta http-equiv")).toBe(true)
    expect(htmlPreviewDoc("<p>x</p>").endsWith("<p>x</p>")).toBe(true)
  })

  test("the CSP blocks every way to send a request lasso would act on", () => {
    expect(HTML_PREVIEW_CSP).toContain("connect-src 'none'")
    expect(HTML_PREVIEW_CSP).toContain("form-action 'none'")
    expect(HTML_PREVIEW_CSP).toContain("frame-src 'none'")
  })

  test("the sandbox never grants lasso's origin", () => {
    expect(HTML_PREVIEW_SANDBOX).not.toContain("allow-same-origin")
    expect(HTML_PREVIEW_SANDBOX).not.toContain("allow-top-navigation")
  })
})
