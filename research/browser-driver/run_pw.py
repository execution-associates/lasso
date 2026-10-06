# /// script
# requires-python = ">=3.11"
# dependencies = ["camoufox[geoip]==0.4.11", "patchright", "playwright"]
# ///
"""Visit PAGES with a Playwright-API driver and dump text + screenshot.
usage: run_pw.py <label> <mode>
 modes: camoufox-headless | camoufox-virtual | patchright-headless | patchright-headful
        | patchright-cdp (attach to lasso-style chrome at $CDP_URL) | playwright-headless"""
import json, os, sys, time, pathlib
sys.path.insert(0, os.path.dirname(__file__))
from pages import PAGES, EXTRACT

label, mode = sys.argv[1], sys.argv[2]
out = pathlib.Path(os.environ.get("RESULTS", "/home/ubuntu/results")) / label
out.mkdir(parents=True, exist_ok=True)

def visit(ctx):
    for name, url, wait in PAGES:
        page = ctx.new_page()
        rec = {"page": name}
        try:
            page.goto(url, timeout=60000)
            time.sleep(wait)
            rec.update(json.loads(page.evaluate(EXTRACT)))
            page.screenshot(path=str(out / f"{name}.png"), full_page=False)
        except Exception as e:
            rec["error"] = repr(e)[:500]
        (out / f"{name}.json").write_text(json.dumps(rec, indent=1))
        print(name, rec.get("title"), rec.get("error", ""), flush=True)
        page.close()

if mode.startswith("camoufox"):
    from camoufox.sync_api import Camoufox
    headless = True if mode == "camoufox-headless" else "virtual"
    kw = {"proxy": {"server": os.environ["PROXY"]}} if os.environ.get("PROXY") else {}
    if os.environ.get("CFX_BIN"): kw["executable_path"] = os.environ["CFX_BIN"]
    with Camoufox(headless=headless, geoip=True, **kw) as browser:
        visit(browser)
elif mode.startswith("patchright"):
    from patchright.sync_api import sync_playwright
    with sync_playwright() as p:
        if mode == "patchright-cdp":
            b = p.chromium.connect_over_cdp(os.environ["CDP_URL"])
            visit(b.contexts[0])
        else:
            ctx = p.chromium.launch_persistent_context(
                "/tmp/pr-profile", channel="chrome", headless=(mode == "patchright-headless"),
                no_viewport=True)
            visit(ctx)
            ctx.close()
elif mode == "playwright-cdp":
    from playwright.sync_api import sync_playwright
    with sync_playwright() as p:
        b = p.chromium.connect_over_cdp(os.environ["CDP_URL"])
        visit(b.contexts[0])
