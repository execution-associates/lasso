# /// script
# requires-python = ">=3.11"
# dependencies = ["camoufox[geoip]==0.4.11"]
# ///
"""Print Camoufox 156 Playwright launch options (fingerprint config in env) as JSON for a node launchServer."""
import json, os, sys
from camoufox.utils import launch_options
kw = {"headless": True}
if os.environ.get("PROXY"): kw.update(proxy={"server": os.environ["PROXY"]}, geoip=True)
o = launch_options(**kw)
json.dump(o, sys.stdout, default=str)
