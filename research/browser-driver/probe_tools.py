# /// script
# requires-python = ">=3.11"
# dependencies = ["mcp>=1.9"]
# ///
"""Call each chrome-devtools-mcp tool lasso agents rely on against $WS_ENDPOINT; report ok/error per tool."""
import asyncio, os, re
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
HTML = "data:text/html,<title>t</title><button onclick=\"document.title='clicked'\">Go</button><input id=i aria-label=name><input type=file aria-label=up><script>console.log('hi')</script>"
async def main():
    p = StdioServerParameters(command="chrome-devtools-mcp", args=["--wsEndpoint", os.environ["WS_ENDPOINT"], "--no-usage-statistics", "--no-performance-crux"], env={**os.environ, "CHROME_DEVTOOLS_MCP_NO_USAGE_STATISTICS": "1"})
    async with stdio_client(p) as (r, w):
        async with ClientSession(r, w) as s:
            await s.initialize()
            async def call(n, a):
                try:
                    res = await s.call_tool(n, a)
                    t = " ".join(getattr(c, "text", f"<{c.type}>") for c in res.content)
                    print(f"{'ERR' if getattr(res, 'is_error', None) or getattr(res, 'isError', None) else 'ok '} {n}: {t[:160]!r}"); return t
                except Exception as e:
                    print(f"EXC {n}: {e!r}"[:200]); return ""
            t = await call("new_page", {"url": "https://example.com/"})
            ids = re.findall(r"^(\d+):.*\[selected\]", t, re.M); pid = int(ids[0]) if ids else 1
            await call("navigate_page", {"pageId": pid, "type": "url", "url": HTML})
            snap = await call("take_snapshot", {"pageId": pid})
            uids = re.findall(r"uid=(\S+) button", snap); iu = re.findall(r"uid=(\S+) textbox", snap); fu = re.findall(r'uid=(\S+) button "up"', snap)
            if iu: await call("fill", {"pageId": pid, "uid": iu[0], "value": "hello"})
            if uids: await call("click", {"pageId": pid, "uid": uids[0]})
            await call("evaluate_script", {"pageId": pid, "function": "() => document.title + '|' + document.getElementById('i').value"})
            open("/tmp/up.txt", "w").write("x")
            if fu: await call("upload_file", {"pageId": pid, "uid": fu[0], "filePath": "/tmp/up.txt"})
            await call("list_console_messages", {"pageId": pid})
            await call("list_network_requests", {"pageId": pid})
            await call("take_screenshot", {"pageId": pid})
            await call("list_pages", {})
asyncio.run(main())
