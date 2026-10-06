# /// script
# requires-python = ">=3.11"
# dependencies = ["camoufox[geoip]==0.4.11", "playwright", "psutil"]
# ///
"""Sum RSS of the browser process tree with 3 example.com tabs open."""
import sys, time, psutil, subprocess
def tree_rss(pid):
    p = psutil.Process(pid); ps = [p] + p.children(recursive=True)
    return sum(x.memory_info().rss for x in ps if x.is_running()) / 2**20, len(ps)
which = sys.argv[1]
if which == "camoufox":
    from camoufox.sync_api import Camoufox
    with Camoufox(headless=True) as b:
        for _ in range(3): b.new_page().goto("https://example.com/")
        time.sleep(3)
        me = psutil.Process()
        kids = [c for c in me.children(recursive=True) if "camoufox" in (c.name() + " ".join(c.cmdline()[:1])).lower()]
        top = [k for k in kids if k.parent() and "camoufox" not in k.parent().name().lower()]
        print("camoufox MiB, procs:", tree_rss(top[0].pid))
else:
    c = subprocess.Popen(["google-chrome", "--headless=new", "--remote-debugging-port=9399", "--user-data-dir=/tmp/memc", "about:blank"], stderr=subprocess.DEVNULL)
    time.sleep(2)
    from playwright.sync_api import sync_playwright
    with sync_playwright() as p:
        b = p.chromium.connect_over_cdp("http://127.0.0.1:9399")
        for _ in range(3): b.contexts[0].new_page().goto("https://example.com/")
        time.sleep(3); print("chrome MiB, procs:", tree_rss(c.pid))
    c.terminate()
