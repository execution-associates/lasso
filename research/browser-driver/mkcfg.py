"""Turn Python-computed Camoufox launch options into a @playwright/mcp config with a persistent profile dir."""
import json, sys
o = json.load(open(sys.argv[1]))
lo = {"executablePath": o["executable_path"], "args": o["args"], "env": o["env"], "firefoxUserPrefs": o["firefox_user_prefs"], "headless": True}
if o.get("proxy"): lo["proxy"] = o["proxy"]
json.dump({"browser": {"browserName": "firefox", "userDataDir": sys.argv[2], "launchOptions": lo}}, open(sys.argv[3], "w"))
