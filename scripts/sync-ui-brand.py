#!/usr/bin/env python3
"""Vendor the web artwork and record its source and output digests."""

import hashlib
import json
import shutil
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
UI = ROOT / "systems/web/ui"


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main() -> None:
    entries = []
    vectors = [("favicon.svg", "public/icon.svg")]
    for kind in ("mark", "lockup"):
        for theme in ("mocha", "latte"):
            name = f"q15-{kind}-{theme}.svg"
            vectors.append((name, f"src/components/brand/{name}"))
    for name, target in vectors:
        source = ROOT / "brand/svg" / name
        output = UI / target
        output.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, output)
        entries.append({"source": name, "target": target,
                        "sourceHash": digest(source), "outputHash": digest(output)})
    source = ROOT / "brand/svg/q15-mark-mocha.svg"
    for size in (192, 512):
        target = f"public/icon-{size}.png"
        output = UI / target
        subprocess.run(["rsvg-convert", "-w", str(size), "-h", str(size),
                        str(source), "-o", str(output)], check=True)
        entries.append({"source": source.name, "target": target,
                        "sourceHash": digest(source), "outputHash": digest(output)})
    (UI / "build/brand-assets.json").write_text(json.dumps(entries, indent=2) + "\n")


if __name__ == "__main__":
    main()
