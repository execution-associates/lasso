// Launch Camoufox as a Playwright server with the SAME playwright as @playwright/mcp (versions must match);
// launch options (binary, fingerprint env, prefs) come from the Python launcher on stdin.
import { createRequire } from 'module';
import fs from 'fs';
const require = createRequire(process.cwd() + '/');
const { firefox } = require('playwright');
const o = JSON.parse(fs.readFileSync(0, 'utf8'));
const s = await firefox.launchServer({
  executablePath: o.executable_path, args: o.args, env: o.env, firefoxUserPrefs: o.firefox_user_prefs,
  headless: o.headless, proxy: o.proxy, port: 9411, wsPath: 'cfx' });
console.log('WS', s.wsEndpoint());
