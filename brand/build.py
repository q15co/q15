#!/usr/bin/env python3
"""Regenerate the q15 brand asset set from the single source artwork.

Source: q15 logo (ASCII duck + wordmark) on the Catppuccin Mocha palette.

Outputs, all transparent, no background tile:

  colour mark   latte / mocha      (no wordmark)      -> hero, avatars, app icons
  colour lockup latte / mocha      (with wordmark)    -> README, og cards, docs
  mono   mark   latte / mocha      (single text fill) -> site header, print
  favicon       latte default, mocha via prefers-color-scheme

Rasters in png/ are rendered from the SVGs below, so the vector files stay the
source of truth and nothing here is hand-edited.

Run from anywhere:

    python3 brand/build.py

Requires rsvg-convert (librsvg) and ImageMagick's `magick` on PATH.
"""

import re
import subprocess
import sys
import tempfile
from pathlib import Path

BRAND = Path(__file__).resolve().parent
SRC = BRAND / "source" / "q15-duck.svg"
SVG_OUT = BRAND / "svg"
PNG_OUT = BRAND / "png"

# Catppuccin v1.8.0: mocha (source artwork) -> latte equivalent.
MOCHA_TO_LATTE = {
    "#f9e2af": "#df8e1d",  # yellow
    "#fab387": "#fe640b",  # peach
    "#89b4fa": "#1e66f5",  # blue
    "#cdd6f4": "#4c4f69",  # text
    "#ffffff": "#eff1f5",  # highlight -> base, so the eye dot reads on the dark eye
}

MONO = {
    "latte": "#4c4f69",  # latte text
    "mocha": "#cdd6f4",  # mocha text
}

GROUPS = ("duck", "beak", "wing-and-eye", "waves", "eye-highlight", "wordmark")

DUCK_DESC = ("A yellow rubber duck formed from programming punctuation, "
             "floating on blue tilde waves.")
LOCKUP_DESC = DUCK_DESC + " The q15 wordmark sits below."

# Rasters that are worth shipping rather than regenerating on each use.
PNG_EXPORTS = (
    ("q15-lockup-mocha.svg", "q15-lockup-1024.png", 1024),
    ("q15-mark-mocha.svg", "q15-mark-1024.png", 1024),
    ("q15-mark-mocha.svg", "q15-mark-512.png", 512),
    ("q15-mark-mocha.svg", "apple-touch-icon-180.png", 180),
)


def group(src: str, name: str) -> str:
    m = re.search(rf'  <g id="{name}".*?</g>\n', src, re.S)
    if not m:
        sys.exit(f"group not found: {name}")
    return m.group(0)


def strip_background(src: str) -> str:
    return re.sub(r'  <rect id="background"[^>]*/>\n', "", src)


def recolour(text: str, mapping: dict[str, str]) -> str:
    for old, new in mapping.items():
        text = text.replace(f'"{old}"', f'"{new}"')
    return text


def build(theme: str, lockup: bool, mono: bool) -> str:
    src = strip_background(SRC.read_text(encoding="utf-8"))

    if mono:
        # One fill everywhere. The eye highlight collapses into the eye, so drop it.
        for name in GROUPS:
            block = group(src, name)
            if name == "eye-highlight":
                src = src.replace(block, "")
                continue
            block2 = re.sub(r'fill="#[0-9a-f]{6}"', f'fill="{MONO[theme]}"', block)
            src = src.replace(block, block2)
        src = re.sub(r'(<g id="\w+" fill=")[^"]+(")', lambda m: m.group(1) + MONO[theme] + m.group(2), src)
    else:
        if theme == "latte":
            src = recolour(src, MOCHA_TO_LATTE)

    if not lockup:
        src = src.replace(group(src, "wordmark"), "")

    return src


def set_meta(svg: str, title: str, desc: str, viewbox: tuple) -> str:
    x, y, w, h = viewbox
    svg = re.sub(r"<title id=\"title\">.*?</title>", f"<title id=\"title\">{title}</title>", svg, flags=re.S)
    svg = re.sub(r"<desc id=\"desc\">.*?</desc>", f"<desc id=\"desc\">{desc}</desc>", svg, flags=re.S)
    svg = re.sub(
        r'width="\d+" height="\d+" viewBox="[^"]+"',
        f'width="{int(w)}" height="{int(h)}" viewBox="{x:g} {y:g} {w:g} {h:g}"',
        svg, count=1,
    )
    return svg


def tight_box(svg: str, pad: float = 0.06) -> tuple:
    """Render, trim, and return a square viewBox centred on the artwork."""
    with tempfile.TemporaryDirectory() as tmpdir:
        tmp = Path(tmpdir) / "_probe.svg"
        png = Path(tmpdir) / "_probe.png"
        tmp.write_text(svg, encoding="utf-8")
        subprocess.run(["rsvg-convert", "-w", "1254", "-h", "1254", str(tmp), "-o", str(png)], check=True)
        geo = subprocess.run(
            ["magick", str(png), "-background", "white", "-flatten", "-trim",
             "-format", "%wx%h%O", "info:"],
            check=True, capture_output=True, text=True,
        ).stdout.strip()
    wh, xo, yo = geo.split("+")[0], geo.split("+")[1], geo.split("+")[2]
    bw, bh = (int(v) for v in wh.split("x"))
    x0, y0 = int(xo), int(yo)
    cx, cy = x0 + bw / 2, y0 + bh / 2
    side = max(bw, bh) * (1 + pad)
    return (round(cx - side / 2, 1), round(cy - side / 2, 1), round(side, 1), round(side, 1))


def write(name: str, svg: str) -> None:
    SVG_OUT.mkdir(parents=True, exist_ok=True)
    (SVG_OUT / name).write_text(svg, encoding="utf-8")


def main() -> None:
    if not SRC.is_file():
        sys.exit(f"source artwork missing: {SRC}")
    PNG_OUT.mkdir(parents=True, exist_ok=True)

    variants = {}

    # Full-colour mark (hero, reusable)
    for theme in ("latte", "mocha"):
        svg = build(theme, lockup=False, mono=False)
        box = tight_box(svg)
        svg = set_meta(svg, "q15 duck mark", DUCK_DESC, box)
        variants[f"q15-mark-{theme}.svg"] = svg

    # Full-colour lockup (mark + wordmark)
    for theme in ("latte", "mocha"):
        svg = build(theme, lockup=True, mono=False)
        svg = set_meta(svg, "q15 logo", LOCKUP_DESC, (0, 0, 1254, 1254))
        variants[f"q15-lockup-{theme}.svg"] = svg

    # Monochrome mark (site header, print)
    for theme in ("latte", "mocha"):
        svg = build(theme, lockup=False, mono=True)
        box = tight_box(svg)
        svg = set_meta(svg, "q15 duck mark", DUCK_DESC, box)
        variants[f"q15-mono-{theme}.svg"] = svg

    # Favicon: colour mark, latte by default, mocha when the OS asks for dark.
    fav = build("latte", lockup=False, mono=False)
    fav = set_meta(fav, "q15", DUCK_DESC, (0, 0, 1254, 1254))
    box = tight_box(fav)
    fav = set_meta(fav, "q15", DUCK_DESC, box)
    # Give the colour groups classes so the media query can flip them.
    for gid, cls in (("duck", "q15-yellow"), ("beak", "q15-peach"),
                     ("waves", "q15-blue"), ("wing-and-eye", "q15-text")):
        fav = fav.replace(f'<g id="{gid}"', f'<g id="{gid}" class="{cls}"')
    style = (
        "  <style>\n"
        "    .q15-yellow { fill: #df8e1d; }\n"
        "    .q15-peach { fill: #fe640b; }\n"
        "    .q15-blue { fill: #1e66f5; }\n"
        "    .q15-text { fill: #4c4f69; }\n"
        "    @media (prefers-color-scheme: dark) {\n"
        "      .q15-yellow { fill: #f9e2af; }\n"
        "      .q15-peach { fill: #fab387; }\n"
        "      .q15-blue { fill: #89b4fa; }\n"
        "      .q15-text { fill: #cdd6f4; }\n"
        "    }\n"
        "  </style>\n"
    )
    fav = fav.replace("</desc>\n", "</desc>\n" + style, 1)
    variants["favicon.svg"] = fav

    for name, svg in variants.items():
        write(name, svg)

    for src_name, png_name, size in PNG_EXPORTS:
        subprocess.run(
            ["rsvg-convert", "-w", str(size), "-h", str(size),
             str(SVG_OUT / src_name), "-o", str(PNG_OUT / png_name)],
            check=True,
        )

    for name in sorted(variants):
        print(f"svg/{name:24s} {len(variants[name]):>6d} bytes")
    for _, png_name, size in PNG_EXPORTS:
        print(f"png/{png_name:24s} {size:>6d} px")


if __name__ == "__main__":
    main()
