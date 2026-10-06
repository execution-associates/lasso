# /// script
# requires-python = ">=3.11"
# dependencies = ["mcp>=1.9"]
# ///
"""stdio @playwright/mcp with config argv[1]; argv[2]=set|check. One session; set a cookie + localStorage, read back."""
import asyncio, os, sys
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
CLI = os.path.expanduser("~/dl/pm/node_modules/@playwright/mcp/cli.js")
def txt(res): return " ".join(getattr(c, "text", "<img>") for c in res.content)
async def main():
    async with stdio_client(StdioServerParameters(command="node", args=[CLI, "--config", sys.argv[1]])) as (r, w):
        async with ClientSession(r, w) as s:
            await s.initialize()
            if sys.argv[2] == "set":
                await s.call_tool("browser_navigate", {"url": "https://example.com/"})
                await s.call_tool("browser_evaluate", {"function": "() => { document.cookie = 'lasso=persist; max-age=86400; path=/'; localStorage.setItem('k','v'); return 1 }"})
            await s.call_tool("browser_navigate", {"url": "https://example.com/"})
            print(sys.argv[2], txt(await s.call_tool("browser_evaluate", {"function": "() => document.cookie + ' | ls=' + localStorage.getItem('k')"}))[:80].replace("\n", " "))
            await s.call_tool("browser_close", {})
asyncio.run(main())
