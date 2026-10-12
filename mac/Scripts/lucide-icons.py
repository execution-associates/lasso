# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Write Lucide SVGs from lucide-react's icon data (lucide-icons.sh pipes it in).

    lucide-icons.py <out dir> <name>...  < {"version", "license", "icons": {name: [[tag, attrs], ...]}}

The JSON comes out of the dev container, so it is data to check, never to
trust: only Lucide's shapes, only geometry attributes, only numbers and path
commands in them. Anything else fails the run and writes nothing.
"""

import json
import re
import sys
from pathlib import Path

PINNED = "1.17.0"  # src/web/bun.lock's lucide-react; bump both together
TAGS = {"path", "rect", "circle", "ellipse", "line", "polyline", "polygon"}
ATTRS = {"d", "x", "y", "x1", "y1", "x2", "y2", "cx", "cy", "r", "rx", "ry", "width", "height", "points"}
VALUE = re.compile(r"^[0-9MmLlHhVvCcSsQqTtAaZz .,\-]{1,4000}$")
NAME = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")


def fail(msg: str) -> None:
    sys.exit(f"lucide-icons: {msg}")


def svg(name: str, version: str, nodes: list) -> str:
    if not isinstance(nodes, list) or not nodes:
        fail(f"{name}: no shapes")
    lines = []
    for node in nodes:
        if not (isinstance(node, list) and len(node) == 2 and isinstance(node[1], dict)):
            fail(f"{name}: malformed node")
        tag, attrs = node
        if tag not in TAGS:
            fail(f"{name}: element {tag!r} is not a Lucide shape")
        parts = []
        for k, v in attrs.items():
            if k == "key":
                continue  # React's list key, not SVG
            if k == "fill" and v in ("currentColor", "none"):
                pass  # a dot drawn solid (key-round's)
            elif k not in ATTRS or not isinstance(v, str) or not VALUE.match(v):
                fail(f"{name}: attribute {k}={v!r} refused")
            parts.append(f'{k}="{v}"')
        lines.append(f"  <{tag} {' '.join(parts)} />")
    head = "\n".join(
        [
            f"<!-- @license lucide v{version} - ISC (lucide-react's {name}) -->",
            "<svg",
            f'  class="lucide lucide-{name}"',
            '  xmlns="http://www.w3.org/2000/svg"',
            '  width="24"',
            '  height="24"',
            '  viewBox="0 0 24 24"',
            '  fill="none"',
            '  stroke="currentColor"',
            '  stroke-width="2"',
            '  stroke-linecap="round"',
            '  stroke-linejoin="round"',
            ">",
        ]
    )
    return head + "\n" + "\n".join(lines) + "\n</svg>\n"


def main() -> None:
    out, names = Path(sys.argv[1]), sys.argv[2:]
    data = json.load(sys.stdin)
    version = data.get("version")
    if version != PINNED:
        fail(f"lucide-react is {version!r}, this script pins {PINNED}")
    license_text = data.get("license", "")
    if not license_text.startswith("ISC License") or not license_text.isascii():
        fail("lucide-react's LICENSE is not the ISC text")
    icons = data.get("icons", {})
    files = {}
    for name in names:
        if not NAME.match(name) or name not in icons:
            fail(f"missing icon {name!r}")
        files[f"{name}.svg"] = svg(name, version, icons[name])
    out.mkdir(parents=True, exist_ok=True)
    for stale in out.glob("*.svg"):
        if stale.name not in files:
            stale.unlink()
    for fname, text in files.items():
        (out / fname).write_text(text)
    (out / "LICENSE").write_text(license_text if license_text.endswith("\n") else license_text + "\n")
    print(f"wrote {len(files)} icons from lucide-react {version} to {out}")


if __name__ == "__main__":
    main()
