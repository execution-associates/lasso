# /// script
# requires-python = ">=3.11"
# dependencies = ["mcp>=1.9"]
# ///
"""Drive @playwright/mcp (config at argv[1]) and exercise the tools lasso agents use; then hit the Cloudflare and dbi test pages."""
import asyncio, os, re, sys
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
CLI = os.path.expanduser("~/dl/pm/node_modules/@playwright/mcp/cli.js")
HTML = "data:text/html,<title>t</title><button onclick=\"document.title='clicked'\">Go</button><input id=i aria-label=name><input type=file aria-label=up><script>console.log('hi')</script>"
async def main():
    p = StdioServerParameters(command="node", args=[CLI, "--config", sys.argv[1]])
    async with stdio_client(p) as (r, w):
        async with ClientSession(r, w) as s:
            await s.initialize()
            tl = (await s.list_tools()).tools
            for t in tl:
                if t.name in ("browser_click","browser_type","browser_file_upload"): print("SCHEMA", t.name, (getattr(t,"input_schema",None) or getattr(t,"inputSchema",None))["properties"].keys())
            async def call(n, a):
                try:
                    res = await s.call_tool(n, a)
                    t = " ".join(getattr(c, "text", f"<{c.type}>") for c in res.content)
                    err = getattr(res, "is_error", None) or getattr(res, "isError", None)
                    print(f"{'ERR' if err else 'ok '} {n}: {t[:220]!r}"); return t
                except Exception as e:
                    print(f"EXC {n}: {e!r}"[:300]); return ""
            await call("browser_navigate", {"url": HTML})
            snap = await call("browser_snapshot", {})
            b = re.findall(r'button "Go" \[ref=(\w+)\]', snap); i = re.findall(r'textbox "name" \[ref=(\w+)\]', snap)
            if i: await call("browser_type", {"target": i[0], "text": "hello"})
            if b: await call("browser_click", {"target": b[0]})
            await call("browser_evaluate", {"function": "() => document.title + '|' + document.getElementById('i').value"})
            u = re.findall(r'button "up" \[ref=(\w+)\]', snap) or re.findall(r'"up" \[ref=(\w+)\]', snap)
            open("/tmp/up.txt","w").write("x")
            if u:
                await call("browser_click", {"target": u[0]})
                await call("browser_file_upload", {"paths": ["/tmp/up.txt"]})
                await call("browser_evaluate", {"function": "() => document.querySelector('input[type=file]').files[0]?.name"})
            await call("browser_console_messages", {})
            await call("browser_network_requests", {})
            await call("browser_take_screenshot", {})
            await call("browser_tabs", {"action": "list"})
asyncio.run(main())
