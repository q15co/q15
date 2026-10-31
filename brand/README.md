# q15 brand

One artwork, generated variants. Nothing in `svg/` or `png/` is hand-edited: every file comes from
`source/q15-duck.svg` through `build.py`, so a change to the artwork or the palette regenerates the
whole set consistently.

The duck is drawn from programming punctuation and floats on tilde waves. It is a rubber duck
because q15 is short for *Quietscheentchen*: fifteen letters, one duck.

## Which file to use

| Need                                          | File                               |
| --------------------------------------------- | ---------------------------------- |
| README, og card, docs header, slide title     | `svg/q15-lockup-{mocha,latte}.svg` |
| Hero image, avatar, app icon, anything square | `svg/q15-mark-{mocha,latte}.svg`   |
| Site header, print, single-colour embroidery  | `svg/q15-mono-{mocha,latte}.svg`   |
| Browser tab, bookmark, home-screen icon       | `svg/favicon.svg`                  |

`mocha` is the dark-background form and the default for anything that ships one file. `latte` is its
light-background equivalent, not a tint of it: each colour is remapped to the same role in the other
Catppuccin flavour.

## Colour

The palette is [Catppuccin](https://catppuccin.com) v1.8.0. Do not introduce colours outside it, and
do not substitute the nearest brand colour.

| Role                | Mocha (dark)     | Latte (light)    |
| ------------------- | ---------------- | ---------------- |
| Duck body           | `#f9e2af` yellow | `#df8e1d` yellow |
| Beak                | `#fab387` peach  | `#fe640b` peach  |
| Waves               | `#89b4fa` blue   | `#1e66f5` blue   |
| Wing, eye, wordmark | `#cdd6f4` text   | `#4c4f69` text   |

The eye highlight is `#ffffff` on Mocha and `#eff1f5` on Latte, kept distinct from the eye itself so
the dot reads at small sizes.

## Size

Measured by rendering each form at decreasing widths, not estimated:

| Form                     | Smallest usable | What fails below it                                                         |
| ------------------------ | --------------- | --------------------------------------------------------------------------- |
| Lockup (mark + wordmark) | **160 px wide** | the `q15` closes up; at 96 px it is barely legible, at 64 px it is a smudge |
| Mark alone               | **24 px**       | the duck's punctuation structure collapses into a coloured speck at 16 px   |
| Favicon                  | **16 px**       | nothing; it is drawn for the tab, so it is the one form that survives 16 px |

Below 160 px, drop the wordmark and use the mark. Below 24 px, use `favicon.svg`. Do not shrink the
lockup to reach a small space.

## Clear space and placement

Leave clear space of a quarter of the mark's height on all four sides, measured from the outermost
point of the duck. Nothing intrudes: no text, no border, no other logo.

Put the asset on a flat background from the palette, or on a dark surface at least as dark as Mocha
base (`#1e1e2e`). The transparent artwork has no plate of its own, so on a busy photograph or a
mid-tone background it disappears.

## Do not

- Recolour the duck. The yellow body is the mark; a blue duck is a different animal.
- Stretch, rotate, outline, add a shadow or a glow.
- Take the wordmark out of the lockup and set it in another typeface. The lettering is part of the
  artwork.
- Place the lockup on Mocha and the mark on Latte in the same surface. One theme per surface, unless
  the surface itself switches theme.
- Redraw any of it. Change `source/q15-duck.svg` and regenerate instead.

## Regenerating

```bash
python3 brand/build.py
```

Requires `rsvg-convert` (librsvg) and ImageMagick's `magick` on `PATH`. The script prints every file
it writes. Output is deterministic: regenerating from an unchanged source reproduces the committed
files byte for byte, which is how the set in this directory was verified.

## Licence

These files travel under the repository's [MIT licence](../LICENSE) like everything else here. That
covers the artwork, not the name: as the README says of the code, a fork should carry its own name
and its own mark, and nothing here grants a right to imply that a derivative is q15.
