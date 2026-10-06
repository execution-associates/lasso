# /// script
# requires-python = ">=3.11"
# dependencies = ["mcp>=1.9"]
# ///
"""Drive camofox-browser through its own stdio MCP: list tools, open dbi + cloudflare test pages, snapshot, click, type, evaluate, screenshot."""
import asyncio, os, re, json
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
MCP = os.path.expanduser("~/dl/cf/node_modules/@askjo/camofox-browser/mcp/server.mjs")
async def main():
    p = StdioServerParameters(command="node", args=[MCP], env={**os.environ, "CAMOFOX_BASE_URL": "http://127.0.0.1:9377", "CAMOFOX_USER_ID": "research"})
    async with stdio_client(p) as (r, w):
        async with ClientSession(r, w) as s:
            await s.initialize()
            tools = (await s.list_tools()).tools
            print("TOOLS:", [t.name for t in tools])
            for t in tools:
                print("  ", t.name, list((getattr(t, "input_schema", None) or getattr(t, "inputSchema", None) or {}).get("properties", {}).keys()))
            async def call(n, a):
                try:
                    res = await s.call_tool(n, a)
                    t = " ".join(getattr(c, "text", f"<{c.type}>") for c in res.content)
                    err = getattr(res, "is_error", None) or getattr(res, "isError", None)
                    print(f"{'ERR' if err else 'ok '} {n}: {t[:300]!r}"); return t
                except Exception as e:
                    print(f"EXC {n}: {e!r}"[:300]); return ""
            t = await call("camofox_create_tab", {"url": "https://www.scrapingcourse.com/cloudflare-challenge"})
            m = re.search(r'"?tab_?[iI]d"?\s*[:=]\s*"?([\w-]+)', t); tab = m.group(1) if m else None
            print("TAB", tab)
            await asyncio.sleep(40)
            await call("camofox_evaluate", {"tabId": tab, "expression": "document.title + ' | ' + document.body.innerText.slice(0,120)"})
            await call("camofox_navigate", {"tabId": tab, "url": "https://deviceandbrowserinfo.com/are_you_a_bot"})
            await asyncio.sleep(10)
            await call("camofox_evaluate", {"tabId": tab, "expression": "(document.body.innerText.match(/\"isBot\": \\w+/)||['?'])[0]"})
            snap = await call("camofox_snapshot", {"tabId": tab})
            await call("camofox_screenshot", {"tabId": tab})
            await call("camofox_list_tabs", {})
            await call("camofox_close_tab", {"tabId": tab})
asyncio.run(main())
