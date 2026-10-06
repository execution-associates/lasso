# /// script
# requires-python = ">=3.11"
# dependencies = ["nodriver==0.48.1"]
# ///
"""Visit PAGES with nodriver (launches Chrome itself). usage: run_nd.py <label> <headless|headful>"""
import asyncio, json, os, sys, pathlib
sys.path.insert(0, os.path.dirname(__file__))
from pages import PAGES, EXTRACT
import nodriver as uc
label, mode = sys.argv[1], sys.argv[2]
out = pathlib.Path(os.environ.get("RESULTS", "/home/ubuntu/results")) / label
out.mkdir(parents=True, exist_ok=True)
async def main():
    b = await uc.start(headless=(mode == "headless"), browser_executable_path="/usr/bin/google-chrome")
    for name, url, wait in PAGES:
        rec = {"page": name}
        try:
            tab = await b.get(url, new_tab=True)
            await tab.sleep(wait)
            raw = await tab.evaluate(f"({EXTRACT})()")
            rec.update(json.loads(raw))
            await tab.save_screenshot(str(out / f"{name}.png"))
            await tab.close()
        except Exception as e:
            rec["error"] = repr(e)[:500]
        (out / f"{name}.json").write_text(json.dumps(rec, indent=1))
        print(name, rec.get("title"), rec.get("error", ""), flush=True)
    b.stop()
asyncio.run(main())
