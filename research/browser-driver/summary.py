import json, re, sys, pathlib
root = pathlib.Path(sys.argv[1])
for d in sorted(p for p in root.iterdir() if p.is_dir() and not p.name.endswith("-chrome")):
    r = {}
    def load(n):
        f = d / f"{n}.json"
        return json.loads(f.read_text()) if f.exists() else {}
    s = load("sannysoft"); r["sanny_fail"] = len(s.get("sanny_failed", [])) if "text" in s else "err"
    r["sanny_rows"] = [x[:60] for x in s.get("sanny_failed", [])][:6]
    c = load("creepjs").get("text", "")
    m = re.search(r"(\d+)% like headless.*?(\d+)% headless.*?(\d+)% stealth", c, re.S)
    r["creep(likeHL/HL/stealth)"] = "/".join(m.groups()) if m else "n/a"
    g = re.search(r"gpu:\n.*\n(.*)\n", c); r["gpu"] = g.group(1)[:50] if g else ""
    sc = re.search(r"\.\.\.screen: (.*)", c); r["screen"] = sc.group(1) if sc else ""
    b = load("browserscan").get("text", ""); m = re.search(r"Test Results:\n(\w+)", b); r["browserscan"] = m.group(1) if m else "n/a"
    t = load("dbi").get("text", ""); m = re.search(r'"isBot": (\w+)', t)
    r["dbi_isBot"] = m.group(1) if m else "n/a"
    r["dbi_true"] = re.findall(r'"(\w+)": true', t)[1:] if m else []
    bd = ""  #.get("text", ""); m = re.search(r"(?i)(bot[^\n]{0,80})", bd); pass
    cf = load("cfchallenge"); ct = cf.get("text",""); r["cloudflare"] = ("challenged" if "Ray ID" in ct else ("PASS" if ct else "n/a")) + " | " + ct[:90].replace("\n"," ")
    r["ua"] = (load("dbi").get("ua") or "")[:110]
    print("=====", d.name); [print(f"  {k}: {v}") for k, v in r.items()]
