---
version: alpha
name: q15
description: Design system for q15, an agent runtime you own and run yourself. Pastel Catppuccin palette, a hairline grid, and a mark drawn from programming punctuation.
---

# q15 design system

## Overview

q15 runs on a machine you own. The interface should look like the machine is well kept: a terminal
someone has lived in for years, not a product page selling one. Calm, dense, and legible at arm's
length on a phone at eleven at night.

Four references, and what each one contributes:

| Reference  | Taken                                                                        | Left behind                                           |
| ---------- | ---------------------------------------------------------------------------- | ----------------------------------------------------- |
| ASCII art  | The mark is drawn from programming punctuation. Everything aligns to a grid. | Novelty fonts, ASCII as decoration, ANSI colour noise |
| UnixPorn   | Hairlines, density, status that is always visible, keyboard before pointer   | Transparency, blur, animation for its own sake        |
| Catppuccin | Pastel palette, two flavours as equals, legibility before aesthetic balance  | Treating mauve and pink as ornament                   |
| Ghibli     | Warmth and daylight, quiet surfaces, a companion rather than a mascot        | Illustration, gradients, scenery, narrative flourish  |
| The 1990s  | Honesty about the machine: a visible cursor, real status text, chunky mono   | Bevels, chrome, drop shadows, skeuomorphic buttons    |

The short form: **Ghibli draws the palette, UnixPorn draws the grid, ASCII draws the mark, and the
nineties keep it honest.**

The result is *calm density*. Nothing on screen is decorative. A surface earns its pixels by
carrying state: what the agent is doing, which machine it is on, what it can reach. Space is tight
but never cramped. Colour is pastel but never sugary, because the palette is doing semantic work
rather than signalling taste.

**Who it is for.** Someone who already runs a server, or who is willing to follow a tutorial to get
one. They want the same experience on a phone, a desktop and a browser tab, and they want the
transcript to stay on their own disk. They are not impressed by dashboards.

**What it should feel like.** A room with daylight in it and a machine humming in the corner. The
agent is present and unremarkable. Nothing glows, nothing pulses to attract attention, nothing
congratulates you for clicking.

### Voice

q15 writes in two registers, and which one you are in is always obvious:

- **The human register** carries prose: documentation, error explanations, release notes, the front
  page. Full sentences, second person for instructions, no jargon that exists only inside the
  codebase.
- **The machine register** carries chrome: labels, statuses, counters, timestamps, tool names.
  Uppercase where it fits a column, no sentence punctuation, no verbs wasted.

Rules that hold in both registers:

1. The first sentence is a fact, and it is short. Adjectives may arrive in the second.
1. State the cost before the benefit: the requirement, the download, the wait, the thing that will
   break. Then say what it buys.
1. Never print a number that has not been measured on a named machine. Label an estimate as one.
1. Every error names the thing that failed, the value that was wrong, and the fix.
1. Every procedure step is followed by what should have happened. "You should now see… If you do
   not, …".
1. No exclamation marks in a claim. They are allowed where a person would use them, which is never
   in a status line.

## Colors

The palette is [Catppuccin](https://catppuccin.com) v1.8.0, unmodified. No colour outside the
palette is introduced, and no nearest-brand substitute is used when a role is missing: fix the role
instead.

Role mapping follows the
[Catppuccin Style Guide](https://github.com/catppuccin/catppuccin/blob/main/docs/style-guide.md),
which is the source for the canvas, pane, control, text, link and status assignments below. q15's
own guide is a distinct document that inherits from it, and departs in three places:

1. **Mauve is the primary accent.** The style guide gives Blue to links and pills. q15 keeps Blue
   for links and tool calls, and gives Mauve to the agent's own state: the primary action, the
   active row, the thing currently thinking.
1. **Every surface carries a hairline.** Catppuccin's guide separates panes by tone alone. q15 adds
   a border of Surface 0 at 55% opacity so that adjacent tones stay distinguishable on dim screens
   and in screenshots.
1. **Latte never sets a status hue as text.** Catppuccin Green on Latte Base measures 2.96:1, below
   the 3:1 floor even for large text. On Latte the status word stays in Text and the hue moves to
   the pill tint or a dot beside it.

**Mocha is the default.** A bare token name is the Mocha value; the `-latte` suffix is the same role
in the light flavour. The two are remapped, not tinted: each one is chosen to hold the same contrast
against its own background.

A translucent value is written as alpha hex, `#3132448c`, which is
`color-mix(in oklab, #313244 55%, transparent)` spelled so every tool parses it.

Two exceptions to that, for different reasons. **Status pill tints are precomputed into opaque
composites** of the hue at 18% over Base, so that a contrast check has a real value to measure; on
Mantle, recompute rather than reuse. **Selection stays translucent**, because it must sit over
whatever is beneath it, and it declares no text colour of its own: the text keeps the colour it
already had.

Roles, with measured contrast against Base:

| Role             | Token             | Mocha     | Latte     | Contrast (Mocha / Latte) |
| ---------------- | ----------------- | --------- | --------- | ------------------------ |
| Canvas           | `surface-base`    | `#1e1e2e` | `#eff1f5` | n/a                      |
| Secondary pane   | `surface-mantle`  | `#181825` | `#e6e9ef` | n/a                      |
| Deepest pane     | `surface-crust`   | `#11111b` | `#dce0e8` | n/a                      |
| Controls         | `surface-control` | `#313244` | `#ccd0da` | n/a                      |
| Body copy        | `text-body`       | `#cdd6f4` | `#4c4f69` | 11.34 / 7.06             |
| Labels, captions | `text-muted`      | `#bac2de` | `#5c5f77` | 9.26 / 5.53              |
| Placeholder      | `text-subtle`     | `#9399b2` | `#7c7f93` | 5.81 / 3.49              |
| Links            | `accent-link`     | `#89b4fa` | `#1e66f5` | 7.79 / 4.34              |
| Primary action   | `primary`         | `#cba6f7` | `#8839ef` | 8.07 / 4.79              |
| Success          | `status-success`  | `#a6e3a1` | `#40a02b` | 11.03 / 2.96             |
| Warning          | `status-warning`  | `#f9e2af` | `#df8e1d` | 12.91 / not as text      |
| Error            | `status-error`    | `#f38ba8` | `#d20f39` | 7.08 / 4.80              |
| Cursor           | `cursor`          | `#f5e0dc` | `#dc8a78` | n/a                      |

The mark owns three colours and does not share them with the interface: Yellow (`#f9e2af` /
`#df8e1d`) for the duck, Peach (`#fab387` / `#fe640b`) for the beak, Blue for the waves. The
wordmark is Text. A Yellow status pill and a Yellow duck are the same ink, and that is the point:
the brand lives in the chrome rather than beside it.

Selection is Overlay 2 at 25%, per the style guide. Disabled text is Overlay 1 on the control
surface, never a hue.

```yaml
colors:
  surface-base: "#1e1e2e"
  surface-base-latte: "#eff1f5"
  surface-mantle: "#181825"
  surface-mantle-latte: "#e6e9ef"
  surface-crust: "#11111b"
  surface-crust-latte: "#dce0e8"
  surface-control: "#313244"
  surface-control-latte: "#ccd0da"
  text-body: "#cdd6f4"
  text-body-latte: "#4c4f69"
  text-muted: "#bac2de"
  text-muted-latte: "#5c5f77"
  text-subtle: "#9399b2"
  text-subtle-latte: "#7c7f93"
  accent-link: "#89b4fa"
  accent-link-latte: "#1e66f5"
  primary: "#cba6f7"
  primary-latte: "#8839ef"
  status-success: "#a6e3a1"
  status-success-latte: "#40a02b"
  status-warning: "#f9e2af"
  status-warning-latte: "#df8e1d"
  status-error: "#f38ba8"
  status-error-latte: "#d20f39"
  cursor: "#f5e0dc"
  cursor-latte: "#dc8a78"
  border-hairline: "#3132448c"
  border-hairline-latte: "#ccd0da8c"
  selection: "#585b7040"
  selection-latte: "#9ca0b040"
```

## Typography

One family, [Recursive](https://www.recursive.design/), bundled locally with its full variation
axes. No font asset makes a third-party request at runtime.

The MONO axis is the whole idea. At `MONO 0` Recursive is a humanist sans, and that is the voice of
prose: documentation, messages, anything a person reads as writing. At `MONO 1` it is monospaced,
and that is the machine register: headings, labels, status, code, sidebar chrome. One family means
the two registers are visibly related rather than merely adjacent, which is the visual form of q15's
voice rule.

CASL stays at the font default of 0. The app does not animate font variation axes. The compact
welcome uses `heading-lg`; the full welcome uses `display`.

Sizes are set in `rem` so that a browser font preference is honoured and the interface scales
instead of clipping. Spacing does not scale with them; controls grow from minimum heights instead.

- **Display** appears on the welcome surface, once. Never inside a conversation.
- **Headings** are machine voice, tightened by `-0.02em`. No heading is set in the human register.
- **Body** is humanist at 1.65 line height, against 1.5 for controls and 1.25 for headings.
- **Labels** are machine voice and may be uppercase. Tracking is opened only when uppercase.
- **Code and tool output** stay monospaced at the same size as body, so a transcript does not jump
  when it switches between prose and output.

```yaml
typography:
  display:
    fontFamily: Recursive
    fontSize: 2.5rem
    fontWeight: 600
    lineHeight: 1.1
    letterSpacing: -0.02em
    fontVariation: "'MONO' 1"
  heading-lg:
    fontFamily: Recursive
    fontSize: 1.5rem
    fontWeight: 600
    lineHeight: 1.25
    letterSpacing: -0.02em
    fontVariation: "'MONO' 1"
  heading-md:
    fontFamily: Recursive
    fontSize: 1.25rem
    fontWeight: 600
    lineHeight: 1.25
    fontVariation: "'MONO' 1"
  heading-sm:
    fontFamily: Recursive
    fontSize: 1rem
    fontWeight: 600
    lineHeight: 1.25
    fontVariation: "'MONO' 1"
  body-md:
    fontFamily: Recursive
    fontSize: 1rem
    fontWeight: 400
    lineHeight: 1.65
    fontVariation: "'MONO' 0"
  body-sm:
    fontFamily: Recursive
    fontSize: 0.875rem
    fontWeight: 400
    lineHeight: 1.65
    fontVariation: "'MONO' 0"
  chrome-md:
    fontFamily: Recursive
    fontSize: 0.875rem
    fontWeight: 400
    lineHeight: 1.5
    fontVariation: "'MONO' 1"
  label-sm:
    fontFamily: Recursive
    fontSize: 0.75rem
    fontWeight: 500
    lineHeight: 1.5
    letterSpacing: 0.06em
    fontVariation: "'MONO' 1"
  code-md:
    fontFamily: Recursive
    fontSize: 0.875rem
    fontWeight: 400
    lineHeight: 1.5
    fontVariation: "'MONO' 1"
```

## Layout

A single reading measure of **48rem**, shared by conversation transcripts, composer and prose.
Sidebars sit outside it; nothing else widens the column. A line of prose at 48rem is about 80
characters at `body-md`, which is where the ASCII discipline and the reading discipline meet.

The spacing scale is 4px based. There is no 6px, no 10px, no 18px: if a gap needs to be different,
it is a mistake in the grouping rather than a new value.

Below 48rem the shell switches to its compact layout, which is the same structure with the sidebar
folded, not a second design. Because the threshold is expressed in `rem`, a doubled browser font
preference folds the layout at the same visual density rather than breaking it. Short viewports drop
the composer footer before they drop the composer.

**Keyboard first.** Every control is reachable and operable without a pointer, the focus ring is
Mauve at the same 2px it always is, and focus order follows the reading order. Pointer affordances
are additions, never the only path.

Density is deliberate. Rows are 32px at `chrome-md`, 24px for dense lists, 44px minimum for anything
that is a touch target. The initial welcome retains its fixed quick-send actions. Other empty
transcript states hold one line of orientation and the composer.

```yaml
spacing:
  space-1: 4px
  space-2: 8px
  space-3: 12px
  space-4: 16px
  space-5: 20px
  space-6: 24px
  space-8: 32px
  space-10: 40px
  space-12: 48px
  measure: 48rem
  row-height: 32px
  row-height-dense: 24px
  touch-target: 44px
  breakpoint-compact: 48rem
```

## Elevation & Depth

**There are no shadows in this system.** A shadow is not in the palette, and a blurred approximation
of one is not either. Depth is a tone step plus a hairline, and that is the only vocabulary:

| Level   | Surface           | Carries                                            |
| ------- | ----------------- | -------------------------------------------------- |
| Crust   | `surface-crust`   | The page behind everything, on Mocha only          |
| Base    | `surface-base`    | The canvas: transcript, prose, the reading surface |
| Mantle  | `surface-mantle`  | Sidebars, panes, and anything that overlays Base   |
| Control | `surface-control` | Inputs, buttons, the composer, code blocks         |

An overlay, whether a menu, a dialog or a tooltip, is Mantle plus a hairline plus the same radius as
the surface it covers. It does not get a darker border to separate it, and it does not get a glow to
lift it. On Latte the luminance sequence inverts: Base is the light canvas, Mantle is a darker pane,
and Control is darker again. Overlays use Mantle in both flavours.

Fills do not change on hover. A hovered control moves its hairline to Overlay 1, a pressed control
swaps its ink from Base to Crust, and a focused control gains the Mauve ring. That keeps interaction
feedback inside the palette instead of inventing a tint for every state.

Motion is the third depth cue and it is short: 150ms for state, 240ms for anything that changes
layout, 420ms only for the welcome reveal. Nothing animates on entry that the reader did not cause,
transcript history does not animate at all, and `prefers-reduced-motion` removes entrance, gesture,
disclosure, decoration, theme and smooth-scroll effects without leaving a broken intermediate state.
Motion never carries information that is not also present when motion is off.

## Shapes

Four nonzero radii, plus `none`. Never a spacing token used as a radius.

| Token  | Value   | Applies to                                          |
| ------ | ------- | --------------------------------------------------- |
| `none` | `0`     | The mark, ASCII blocks, box-drawn output, hairlines |
| `sm`   | `4px`   | Tags, inline code, small badges                     |
| `md`   | `8px`   | Inputs, buttons, code blocks, tool-call rows        |
| `lg`   | `12px`  | Cards, panels, dialogs                              |
| `full` | `999px` | Pills, avatars, status dots                         |

Rounded surfaces are the human register and sharp ones are the machine. A code block may be `md`
because it is a container, but its contents are never rounded, and neither is a table, an ASCII
diagram or the duck. A radius never scales with size: a 44px button and a 12px tag each take their
own token, and nothing is a proportion of anything else.

```yaml
rounded:
  none: 0
  sm: 4px
  md: 8px
  lg: 12px
  full: 999px
```

## Components

The browser modules implement these names:

| Design component                                     | Browser module / selector                                                           |
| ---------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `composer`                                           | `composer.module.css` / `.composer`                                                 |
| `message-user`, `message-agent`                      | `message.module.css` / `.userMessage .messageBody`, `.messageBody`                  |
| `tool-call`, `tool-call-active`                      | `activity.module.css` / `.tool`, `.tool[data-running]`                              |
| `code-block`                                         | `markdown.module.css` / `.markdown pre`; `toolcall.module.css` / `.shell`, `.block` |
| `sidebar-row`, `sidebar-row-active`                  | `app.module.css` / `.navItem`, `.navActive`                                         |
| `input`                                              | `login.module.css` / `.enrollment textarea`; `composer.module.css` / `textarea`     |
| `button-primary`, `button-secondary`, `button-ghost` | `ui/button.module.css` / `.default`, `.outline`, `.ghost`                           |
| `status-pill-*`                                      | `activity.module.css` / `.status[data-status]`; message, media and app error labels |
| `tooltip`                                            | Native browser `title` affordances; no custom tooltip surface                       |

Each component is written once per flavour. The `-latte` variant is the same component in the light
palette, not a tint of the dark one.

```yaml
components:
  canvas:
    backgroundColor: "{colors.surface-base}"
    textColor: "{colors.text-body}"
    typography: "{typography.body-md}"
  canvas-latte:
    backgroundColor: "{colors.surface-base-latte}"
    textColor: "{colors.text-body-latte}"
  pane:
    backgroundColor: "{colors.surface-mantle}"
    textColor: "{colors.text-muted}"
    rounded: "{rounded.lg}"
  pane-latte:
    backgroundColor: "{colors.surface-mantle-latte}"
    textColor: "{colors.text-muted-latte}"
    rounded: "{rounded.lg}"
  pane-deep:
    backgroundColor: "{colors.surface-crust}"
  pane-deep-latte:
    backgroundColor: "{colors.surface-crust-latte}"
  control:
    backgroundColor: "{colors.surface-control}"
    textColor: "{colors.text-body}"
    typography: "{typography.chrome-md}"
    rounded: "{rounded.md}"
    height: 32px
  control-latte:
    backgroundColor: "{colors.surface-control-latte}"
    textColor: "{colors.text-body-latte}"
    rounded: "{rounded.md}"
  divider:
    backgroundColor: "{colors.border-hairline}"
    height: 1px
  divider-latte:
    backgroundColor: "{colors.border-hairline-latte}"
    height: 1px
  text-selection:
    backgroundColor: "{colors.selection}"
  text-selection-latte:
    backgroundColor: "{colors.selection-latte}"
  text-cursor:
    backgroundColor: "{colors.cursor}"
    width: 2px
  text-cursor-latte:
    backgroundColor: "{colors.cursor-latte}"
    width: 2px
  placeholder:
    textColor: "{colors.text-subtle}"
    typography: "{typography.body-md}"
  placeholder-latte:
    textColor: "{colors.text-subtle-latte}"
    typography: "{typography.body-md}"
  link:
    textColor: "{colors.accent-link}"
    typography: "{typography.body-md}"
  link-latte:
    textColor: "{colors.accent-link-latte}"
    typography: "{typography.body-md}"
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.surface-base}"
    typography: "{typography.chrome-md}"
    rounded: "{rounded.md}"
    padding: 12px
    height: 36px
  button-primary-latte:
    backgroundColor: "{colors.primary-latte}"
    textColor: "{colors.surface-base-latte}"
    typography: "{typography.chrome-md}"
    rounded: "{rounded.md}"
    padding: 12px
    height: 36px
  button-primary-hover:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.surface-base}"
    rounded: "{rounded.md}"
  button-primary-pressed:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.surface-crust}"
    rounded: "{rounded.md}"
  button-secondary:
    backgroundColor: "{colors.surface-control}"
    textColor: "{colors.text-body}"
    typography: "{typography.chrome-md}"
    rounded: "{rounded.md}"
    padding: 12px
    height: 36px
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.text-muted}"
    typography: "{typography.chrome-md}"
    rounded: "{rounded.md}"
    padding: 12px
    height: 36px
  input:
    backgroundColor: "{colors.surface-control}"
    textColor: "{colors.text-body}"
    typography: "{typography.body-md}"
    rounded: "{rounded.md}"
    padding: 12px
  composer:
    backgroundColor: "{colors.surface-control}"
    textColor: "{colors.text-body}"
    typography: "{typography.body-md}"
    rounded: "{rounded.lg}"
    padding: 16px
    width: "{spacing.measure}"
  message-user:
    backgroundColor: "{colors.surface-control}"
    textColor: "{colors.text-body}"
    typography: "{typography.body-md}"
    rounded: "{rounded.lg}"
    padding: 12px
  message-agent:
    backgroundColor: "transparent"
    textColor: "{colors.text-body}"
    typography: "{typography.body-md}"
    padding: 12px
  tool-call:
    backgroundColor: "{colors.surface-control}"
    textColor: "{colors.text-muted}"
    typography: "{typography.code-md}"
    rounded: "{rounded.md}"
    padding: 8px
  tool-call-active:
    backgroundColor: "{colors.surface-control}"
    textColor: "{colors.accent-link}"
    typography: "{typography.code-md}"
    rounded: "{rounded.md}"
  code-block:
    backgroundColor: "{colors.surface-control}"
    textColor: "{colors.text-body}"
    typography: "{typography.code-md}"
    rounded: "{rounded.md}"
    padding: 12px
  sidebar-row:
    backgroundColor: "transparent"
    textColor: "{colors.text-muted}"
    typography: "{typography.chrome-md}"
    rounded: "{rounded.md}"
    padding: 8px
    height: "{spacing.row-height}"
  sidebar-row-active:
    backgroundColor: "{colors.surface-control}"
    textColor: "{colors.text-body}"
    typography: "{typography.chrome-md}"
    rounded: "{rounded.md}"
    height: "{spacing.row-height}"
  status-pill-success:
    backgroundColor: "#364143"
    textColor: "{colors.status-success}"
    typography: "{typography.label-sm}"
    rounded: "{rounded.full}"
    padding: 4px
  status-pill-success-latte:
    backgroundColor: "#d0e2d1"
    textColor: "{colors.text-body-latte}"
    typography: "{typography.label-sm}"
    rounded: "{rounded.full}"
    padding: 4px
  status-pill-warning:
    backgroundColor: "#454145"
    textColor: "{colors.status-warning}"
    typography: "{typography.label-sm}"
    rounded: "{rounded.full}"
    padding: 4px
  status-pill-warning-latte:
    backgroundColor: "#ecdfce"
    textColor: "{colors.text-body-latte}"
    typography: "{typography.label-sm}"
    rounded: "{rounded.full}"
    padding: 4px
  status-pill-error:
    backgroundColor: "#443244"
    textColor: "{colors.status-error}"
    typography: "{typography.label-sm}"
    rounded: "{rounded.full}"
    padding: 4px
  status-pill-error-latte:
    backgroundColor: "#eac8d3"
    textColor: "{colors.text-body-latte}"
    typography: "{typography.label-sm}"
    rounded: "{rounded.full}"
    padding: 4px
  status-dot-success:
    backgroundColor: "{colors.status-success}"
    rounded: "{rounded.full}"
    size: 6px
  status-dot-success-latte:
    backgroundColor: "{colors.status-success-latte}"
    rounded: "{rounded.full}"
    size: 6px
  status-dot-warning:
    backgroundColor: "{colors.status-warning}"
    rounded: "{rounded.full}"
    size: 6px
  status-dot-warning-latte:
    backgroundColor: "{colors.status-warning-latte}"
    rounded: "{rounded.full}"
    size: 6px
  status-dot-error:
    backgroundColor: "{colors.status-error}"
    rounded: "{rounded.full}"
    size: 6px
  status-dot-error-latte:
    backgroundColor: "{colors.status-error-latte}"
    rounded: "{rounded.full}"
    size: 6px
  tooltip:
    backgroundColor: "{colors.surface-mantle}"
    textColor: "{colors.text-body}"
    typography: "{typography.chrome-md}"
    rounded: "{rounded.sm}"
    padding: 8px
```

A component not listed here is composed from the tokens above rather than invented: pick the surface
from the depth table, the text colour by role, the radius from the shape scale, and the type by
register.

## Do's and Don'ts

- Do let one accent appear once per element. A row is blue or mauve or green, never two of them.
- Don't add a colour. If a role is missing from the palette, the role is wrong.
- Do keep an unbroken hairline between adjacent tones. Without it, two steps of Mocha disappear on a
  cheap panel in daylight.
- Don't use a shadow, a blur, a gradient or a glow. The palette does not contain one, and a
  `box-shadow` is the fastest way to make q15 look like a dashboard.
- Do keep prose humanist (`MONO 0`) and chrome monospaced (`MONO 1`). Mixing them in one sentence
  breaks the register.
- Don't set a heading in the human register, and don't set body copy in the machine one.
- Do keep Latte status text in Text over a hue tint. Latte Green as text measures 2.96:1 and is
  never acceptable, at any size.
- Don't ship a light theme that is a tint of the dark one. Remap each role to the equivalent role in
  the other flavour.
- Do keep the mark unrounded, unstretched and unanimated. It is punctuation that happens to look
  like a duck, not a mascot.
- Don't put the wordmark in a typeface. The lettering is part of the artwork.
- Do measure before printing a number in copy, and label anything that is not measured.
- Don't animate a status change the reader did not cause, and never let motion carry information
  that static rendering lacks.
- Do keep the transcript still. History does not animate on entry, and the reader's scroll position
  is preserved when live content grows.

## References

- Palette: [Catppuccin](https://catppuccin.com) v1.8.0,
  [palette values](https://github.com/catppuccin/palette).
- Role mapping inspired by the
  [Catppuccin Style Guide](https://github.com/catppuccin/catppuccin/blob/main/docs/style-guide.md).
  This document is q15's own and deviates as described under Colors.
- Typeface: [Recursive](https://www.recursive.design/), bundled locally with its full variation
  axes.
- Format: [DESIGN.md](https://github.com/google-labs-code/design.md) (`alpha`).
- Assets, minimum sizes and regeneration: [`brand/README.md`](README.md).
