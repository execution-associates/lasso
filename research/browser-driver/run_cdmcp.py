# /// script
# requires-python = ">=3.11"
# dependencies = ["mcp>=1.9"]
# ///
"""Drive a Chrome launched with lasso's exact flags through chrome-devtools-mcp,
the way lasso's /browser-mcp does. usage: run_cdmcp.py <label> <headless|headful> [extra chrome args...]"""
import asyncio, json, os, sys, time, subprocess, pathlib, shutil
sys.path.insert(0, os.path.dirname(__file__))
from pages import PAGES, EXTRACT
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client

label, mode, extra = sys.argv[1], sys.argv[2], sys.argv[3:]
out = pathlib.Path(os.environ.get("RESULTS", "/home/ubuntu/results")) / label
out.mkdir(parents=True, exist_ok=True)
prof = pathlib.Path("/tmp/lasso-profile-" + label)
shutil.rmtree(prof, ignore_errors=True)
major = subprocess.check_output(["google-chrome", "--version"], text=True).split()[2].split(".")[0]
ua = f"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/{major}.0.0.0 Safari/537.36"
args = ["google-chrome", "--remote-debugging-port=0", f"--user-data-dir={prof}", "--no-first-run",
        "--no-default-browser-check", "--window-size=1280,800",
        "--disable-blink-features=AutomationControlled", "--force-device-scale-factor=2"]
if mode == "headless":
    args = args[:1] + ["--headless=new"] + args[1:] + [f"--user-agent={ua}"]
if os.environ.get("PROXY"): extra = extra + ["--proxy-server=" + os.environ["PROXY"]]
args += extra + ["about:blank"]
if os.environ.get("WS_ENDPOINT"):
    ws = os.environ["WS_ENDPOINT"]; chrome = None
else:
  chrome = subprocess.Popen(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
  for _ in range(150):
      f = prof / "DevToolsActivePort"
      if f.exists() and len(f.read_text().split()) >= 2:
          break
      time.sleep(0.1)
  port, path = f.read_text().split()[:2]
  ws = f"ws://127.0.0.1:{port}{path}"
  print("chrome at", ws, flush=True)
  if os.environ.get("CDP_ONLY"):
      pathlib.Path("/tmp/cdp_url").write_text(f"http://127.0.0.1:{port}")
      chrome.wait(); sys.exit()

async def main():
    params = StdioServerParameters(command="chrome-devtools-mcp", args=[
        "--wsEndpoint", ws, "--no-usage-statistics", "--no-performance-crux",
        "--screenshotFormat", "png"], env={**os.environ, "CHROME_DEVTOOLS_MCP_NO_USAGE_STATISTICS": "1"})
    async with stdio_client(params) as (r, w):
        async with ClientSession(r, w) as s:
            await s.initialize()
            for name, url, wait in PAGES:
                rec = {"page": name}
                try:
                    res = await s.call_tool("new_page", {"url": url, "timeout": 60000})
                    txt = res.content[0].text
                    # pageId of the newest page is the selected one
                    import re
                    ids = re.findall(r"^(\d+):.*\[selected\]", txt, re.M)
                    pid = int(ids[0]) if ids else None
                    time.sleep(wait)
                    a = {"function": EXTRACT}
                    if pid is not None: a["pageId"] = pid
                    ev = await s.call_tool("evaluate_script", a)
                    t = ev.content[0].text
                    m = re.search(r"```json\n(.*)\n```", t, re.S)
                    raw = json.loads(m.group(1)) if m else t
                    rec.update(json.loads(raw) if isinstance(raw, str) else raw)
                    sa = {"filePath": str(out / f"{name}.png")}
                    if pid is not None: sa["pageId"] = pid
                    await s.call_tool("take_screenshot", sa)
                    ca = {"pageId": pid} if pid is not None else {}
                    await s.call_tool("close_page", ca)
                except Exception as e:
                    rec["error"] = repr(e)[:500]
                (out / f"{name}.json").write_text(json.dumps(rec, indent=1))
                print(name, rec.get("title"), rec.get("error", ""), flush=True)
asyncio.run(main())
chrome and chrome.terminate()
