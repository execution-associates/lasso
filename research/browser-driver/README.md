# Browser driver research harness (throwaway)

Throwaway scripts behind the browser-driver comparison report. Not part of
lasso; nothing here is built or shipped. Run only inside an isb VM: they
download third-party browsers and packages.

- `pages.py`: the neutral bot-detection pages and the in-page extractor.
- `run_cdmcp.py`: Chrome launched with lasso's exact `browserArgs`, driven
  through chrome-devtools-mcp 1.10.1 the way `/browser-mcp` does.
  `WS_ENDPOINT` points it at any other CDP server (Lightpanda, Obscura),
  `PROXY` adds `--proxy-server`, `CDP_ONLY` just launches Chrome.
- `run_pw.py`: Playwright-API drivers: Camoufox (headless or virtual display),
  Patchright launched or attached over CDP (`attach.sh`).
- `run_nd.py`: nodriver.
- `probe_tools.py`, `pwmcp_probe.py`, `cf_mcp.py`: per-tool compatibility of
  chrome-devtools-mcp, @playwright/mcp and camofox-browser's MCP.
- `cfx_opts.py` + `cfx_server.mjs`: Camoufox 156 as a Playwright server, with
  launch options from the Python launcher (camoufox-js only supports the 152
  line) and the playwright-core that @playwright/mcp uses.
- `mkcfg.py` + `persist_probe.py`: @playwright/mcp launching Camoufox itself on
  a persistent profile dir.
- `summary.py`, `results-summary.txt`: the per-configuration results.
