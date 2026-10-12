# /// script
# requires-python = ">=3.11"
# dependencies = ["playwright==1.55.0"]
# ///
"""Run as ubuntu inside the readme-shots VM, after seed.sh and terminals.sh.
Screenshots each README cell at 1280x800 (2x) from the demo lasso on
127.0.0.1:8090, with this VM's own headless Chromium, and writes each as a
1600x1000 WebP.

usage: uv run shoot.py OUT_DIR [NAME...]
"""
import json
import re
import subprocess
import sys
from pathlib import Path

from playwright.sync_api import Browser, Page, sync_playwright

UI = "http://127.0.0.1:8090"
STATE = Path.home() / "readme-shots"
TERM = STATE / "term"
BACKDROP = f"{UI}/wallpapers/execution-associates/01-golden-hour.webp"
PW = None  # the Playwright instance, set by main


def lasso(*args: str) -> dict:
    out = subprocess.run(["lasso", "mcp", *args], check=True, capture_output=True, text=True).stdout
    return json.loads(out)


def agent(key: str) -> dict:
    """One of seed.sh's agents, as get_agent reads it now."""
    agent_id, host, *_ = (STATE / "agents" / key).read_text().split()
    a = lasso("get-agent", "-to", agent_id, "-host", host)["agent"]
    a["host"] = host
    return a


def focus(page: Page, a: dict) -> None:
    # Puts the agent's pane in front in herdr, for this tab's host; the
    # terminal, Chat and the sidebar all follow herdr's focus.
    page.evaluate(
        """([pane, host]) => fetch('/api/focus', {method: 'POST',
            headers: {'content-type': 'application/json', 'X-Lasso-Host': host},
            body: JSON.stringify({pane_id: pane, reveal: true})})""",
        [a["root_pane"], a["host"]],
    )
    page.wait_for_timeout(2500)


def open_tab(page: Page, name: str) -> None:
    # The sidebar's tabs are tabs; a pane's own toggles (Files/Diff) are buttons.
    tab = page.get_by_role("tab", name=name, exact=True).or_(page.get_by_role("button", name=name, exact=True))
    tab.filter(visible=True).first.click()
    page.wait_for_timeout(1500)


def terminal_settled(page: Page, tries: int = 20) -> bool:
    """Whether the herdr terminal shows at its normal size. Now and then it
    comes up, or later turns, at two to four times the font with that many
    fewer columns, in this headless Chromium: xterm's cell is ~8 px wide at
    the default 14 px. A page with no terminal frame passes."""
    cell = 0.0
    for _ in range(tries):
        frame = next((f for f in page.frames if "/terminal/" in f.url), None)
        if not frame:
            return True
        cell = frame.evaluate("() => window.term && term.cols ? innerWidth / term.cols : 0")
        if 6 <= cell <= 10:
            return True
        page.wait_for_timeout(500)
    print(f"  terminal not at its normal size (cell {cell:.1f} px wide)", file=sys.stderr)
    return False


def load(page: Page, path: str, a: dict | None = None) -> None:
    # The host is a property of the browser tab and outlives a navigation, so
    # every load names one.
    page.goto(f"{UI}{path}?host={a['host'] if a else 'local'}", wait_until="load")
    # The UI streams live data, so the network never goes idle: give the
    # terminal and the transcripts a moment instead.
    page.wait_for_timeout(3500)
    if a:
        focus(page, a)


# ---- the cells ------------------------------------------------------------------------


def sidebar(collapsed: bool) -> None:
    # The right sidebar's state is synced through the server, so it is set
    # there, and every open page follows.
    lasso("update-settings", "-ui", json.dumps({"sidebar_collapsed": collapsed}))


def terminal(page: Page) -> None:
    # The whole width for herdr: its workspaces, the agent's pane and its
    # agents list.
    sidebar(True)
    load(page, "/", agent("tide"))


def grid(page: Page) -> None:
    sidebar(True)
    load(page, "/agents")
    page.get_by_placeholder("Name, repo, transcript…").wait_for()
    page.wait_for_timeout(3000)


def chat(page: Page) -> None:
    load(page, "/chat", agent("ledger"))
    page.wait_for_timeout(4000)


def files(page: Page) -> None:
    tide = agent("tide")
    load(page, "/", tide)
    # The README, opened the way an agent's open_file would: rendered, with
    # its diagram.
    subprocess.run(["lasso", "open", f"{tide['work_dir']}/README.md"], check=True, capture_output=True)
    page.wait_for_timeout(4000)


def browser(page: Page) -> None:
    # Click the agent's new toggle in the shared browser itself, over lasso's
    # /cdp, as a person clicking in the Browser tab would: the tab streams the
    # page in its dark mode. Disconnecting leaves that browser running.
    shared = PW.chromium.connect_over_cdp(f"{UI}/cdp")
    for p in (p for c in shared.contexts for p in c.pages if ":5173" in p.url):
        toggle = p.get_by_role("button", name=re.compile("dark", re.I))
        if toggle.count():
            toggle.first.click()
    shared.close()
    load(page, "/", agent("tide"))
    open_tab(page, "Browser")
    page.wait_for_timeout(6000)


def new_agent(page: Page) -> None:
    # A git agent on another machine: the host picker, then that host's repos.
    # Over the Grid, the fleet the new agent joins.
    load(page, "/agents")
    # The Grid's own New, not the footer's.
    page.get_by_role("button", name="New", exact=True).first.click()
    page.get_by_role("tab", name=re.compile(r"^Git")).click()
    page.locator("[role=dialog] select[aria-label=Host]").filter(visible=True).select_option(label="buildbox · ci")
    # The host's repositories load once it is picked.
    page.wait_for_timeout(3000)
    page.locator("[role=dialog] select:has(option:text-is('Oh My Pi'))").filter(visible=True).select_option(
        label="Oh My Pi"
    )
    page.locator("[role=dialog] input[placeholder=default]").filter(visible=True).fill("deepseek/deepseek-flash")
    page.locator("[role=dialog] textarea").filter(visible=True).fill(
        "Add a CSV export of each settlement batch: one row per transaction, with its batch number."
    )
    page.wait_for_timeout(1500)


def hosts(page: Page) -> None:
    # Over the Grid, whose cards are grouped by the same machines.
    load(page, "/agents")
    page.get_by_role("button", name="Switch host").click()
    page.wait_for_timeout(2500)


def mcp(page: Page) -> None:
    page.goto(f"file://{TERM}/mcp.html", wait_until="load")
    page.wait_for_timeout(1500)


def bots(page: Page) -> None:
    load(page, "/bots/harbormaster")
    page.wait_for_timeout(4000)


def plugins(page: Page) -> None:
    load(page, "/agents")
    open_tab(page, "Settings")
    page.get_by_text("Plugins", exact=True).first.click()
    page.wait_for_timeout(1000)
    page.get_by_role("button", name="Enable…").first.click()
    page.wait_for_timeout(1500)


def phone(browser: Browser, out: Path) -> None:
    """Two phone screens side by side on the backdrop: the blocked agent's
    question in Chat, and in its terminal with the input dial open."""
    ctx = browser.new_context(
        viewport={"width": 390, "height": 844}, device_scale_factor=3, is_mobile=True, has_touch=True,
        color_scheme="dark",
    )
    page = ctx.new_page()
    # On a phone an open sidebar covers the whole screen.
    sidebar(True)
    load(page, "/chat", agent("ledger"))
    page.wait_for_timeout(3000)
    page.screenshot(path=str(out / "phone-chat.png"))
    # The same agent's question again, in its terminal, with the dial's view
    # picker open over it.
    for _ in range(4):
        load(page, "/", agent("ledger"))
        # The input dial lives inside the terminal's own document.
        page.frame_locator("#term").locator(".dial-root").tap()
        page.wait_for_timeout(2000)
        if terminal_settled(page, 4):
            break
    page.screenshot(path=str(out / "phone-term.png"))
    ctx.close()
    html = out / "phone.html"
    html.write_text(f"""<!doctype html><html><head><meta charset="utf-8"><style>
html,body{{margin:0;height:100%}}
body{{background:linear-gradient(rgba(18,7,31,.35),rgba(18,7,31,.35)),url("{BACKDROP}") center/cover;
  display:flex;align-items:center;justify-content:center;gap:56px}}
img{{height:700px;border-radius:38px;border:7px solid #0d0616;box-shadow:0 24px 60px rgba(0,0,0,.6)}}
</style></head><body><img src="phone-chat.png"><img src="phone-term.png"></body></html>""")


SHOTS = {
    "terminal": terminal,
    "grid": grid,
    "chat": chat,
    "files": files,
    "browser": browser,
    "new-agent": new_agent,
    "hosts": hosts,
    "mcp": mcp,
    "bots": bots,
    "plugins": plugins,
    # Last: a phone-sized terminal narrows herdr's panes for every client for
    # a while after it detaches, which would spoil a desktop cell after it.
    "phone": None,
}


# The cells where the herdr terminal is on screen, rather than covered by
# Chat, the Grid or Bots.
SHOWS_TERMINAL = {"terminal", "files", "browser"}


def main() -> None:
    out = Path(sys.argv[1])
    out.mkdir(parents=True, exist_ok=True)
    names = sorted(sys.argv[2:] or SHOTS, key=list(SHOTS).index)
    global PW
    with sync_playwright() as p:
        PW = p
        b = p.chromium.launch()
        ctx = b.new_context(viewport={"width": 1280, "height": 800}, device_scale_factor=2, color_scheme="dark")
        for name in names:
            page = ctx.new_page()
            if name == "phone":
                phone(b, out)
                page.goto(f"file://{out}/phone.html", wait_until="load")
                page.wait_for_timeout(1000)
            else:
                # A cell whose terminal is off its normal size is taken again
                # on a fresh page, up to four times in all; the last is kept.
                for attempt in range(4):
                    SHOTS[name](page)
                    if attempt == 3 or name not in SHOWS_TERMINAL or terminal_settled(page, 4):
                        break
                    page.close()
                    sidebar(False)
                    page = ctx.new_page()
            png = out / f"{name}.png"
            page.screenshot(path=str(png))
            page.close()
            # A cell that closed the sidebar opens it again for the next.
            sidebar(False)
            # 1600x1000 WebP: sharp at the grid's cell width on a 2x screen.
            # Busy cells (a terminal) compress worse than calm ones,
            # so cwebp aims at a size rather than a quality.
            subprocess.run(
                ["cwebp", "-quiet", "-size", "100000", "-pass", "6", "-resize", "1600", "1000",
                 str(png), "-o", str(out / f"{name}.webp")],
                check=True,
            )
            print(f"shot {name}")
        b.close()


if __name__ == "__main__":
    main()
