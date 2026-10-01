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
