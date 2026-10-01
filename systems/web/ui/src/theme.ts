export type Theme = "mocha" | "latte";
export function initialTheme(): Theme {
  try {
    const saved = localStorage.getItem("q15-theme");
    if (saved === "mocha" || saved === "latte") return saved;
  } catch {
    /* Storage is optional. */
  }
  return matchMedia("(prefers-color-scheme: light)").matches ? "latte" : "mocha";
}
export function applyTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme;
  document
    .querySelector('meta[name="theme-color"]')
    ?.setAttribute("content", theme === "mocha" ? "#1e1e2e" : "#eff1f5");
  try {
    localStorage.setItem("q15-theme", theme);
  } catch {
    /* Private browsing still works. */
  }
}

export function revealTheme(button: HTMLElement, reduced: boolean, update: () => void) {
  if (reduced || !document.startViewTransition) {
    update();
    return;
  }
  const { x, y, width, height } = button.getBoundingClientRect();
  const centerX = x + width / 2;
  const centerY = y + height / 2;
  const radius = Math.hypot(
    Math.max(centerX, innerWidth - centerX),
    Math.max(centerY, innerHeight - centerY),
  );
  const transition = document.startViewTransition(update);
  void transition.ready
    .then(() => {
      // Honor a preference changed while the browser was taking its snapshots.
      if (matchMedia("(prefers-reduced-motion: reduce)").matches) {
        transition.skipTransition();
        return;
      }
      document.documentElement.animate(
        {
          clipPath: [
            `circle(0px at ${centerX}px ${centerY}px)`,
            `circle(${radius}px at ${centerX}px ${centerY}px)`,
          ],
        },
        {
          duration: 420,
          easing: "cubic-bezier(0.22, 1, 0.36, 1)",
          pseudoElement: "::view-transition-new(root)",
        },
      );
    })
    .catch(() => {
      /* Overlapping theme changes may skip a snapshot; the palette still updates. */
    });
}
