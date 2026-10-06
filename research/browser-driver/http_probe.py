# /// script
# requires-python = ">=3.11"
# dependencies = ["mcp>=1.9"]
# ///
"""argv: step. 'set': client A sets a cookie, client B (2nd session) lists tabs + reads cookies. 'check': read cookies after restart."""
import asyncio, sys
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client
URL = "http://localhost:9500/mcp"
async def sess(fn):
    async with streamable_http_client(URL) as streams:
        r, w = streams[0], streams[1]
        async with ClientSession(r, w) as s:
            await s.initialize(); return await fn(s)
def txt(res): return " ".join(getattr(c, "text", "<img>") for c in res.content)
async def main():
    if sys.argv[1] == "set":
        async def a(s):
            print("A nav:", txt(await s.call_tool("browser_navigate", {"url": "https://httpbin.org/cookies/set?lasso=persist"}))[-160:].replace("\n", " "))
        await sess(a)
    async def b(s):
        print("B tabs:", txt(await s.call_tool("browser_tabs", {"action": "list"}))[:200].replace("\n", " "))
        await s.call_tool("browser_navigate", {"url": "https://httpbin.org/cookies"})
        print("B cookies:", txt(await s.call_tool("browser_evaluate", {"function": "() => document.body.innerText"}))[:120].replace("\n", " "))
    await sess(b)
asyncio.run(main())
