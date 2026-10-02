import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { applyTheme, initialTheme, revealTheme } from "./theme";

beforeEach(() => localStorage.clear());
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("theme preference", () => {
  it("prefers a valid saved choice and falls back to the system for missing/invalid choices", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: true }));
    expect(initialTheme()).toBe("latte");
    localStorage.setItem("q15-theme", "invalid");
    expect(initialTheme()).toBe("latte");
    localStorage.setItem("q15-theme", "mocha");
    expect(initialTheme()).toBe("mocha");
    localStorage.setItem("q15-theme", "latte");
    expect(initialTheme()).toBe("latte");
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    localStorage.clear();
    expect(initialTheme()).toBe("mocha");
  });

  it("updates page chrome even when storage is blocked", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: true }));
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(initialTheme()).toBe("latte");
    const meta = document.createElement("meta");
    meta.name = "theme-color";
    document.head.append(meta);
    applyTheme("mocha");
    expect(meta.content).toBe("#1e1e2e");
    applyTheme("latte");
    expect(meta.content).toBe("#eff1f5");
    meta.remove();
  });

  it("uses a circular reveal, honors a newly changed preference and tolerates skipped snapshots", async () => {
    const button = document.createElement("button");
    const animate = vi.fn<Element["animate"]>();
    const skipTransition = vi.fn<() => void>();
    const update = vi.fn<() => void>();
    let reduced = false;
    vi.stubGlobal("matchMedia", () => ({ matches: reduced }));
    vi.stubGlobal("innerWidth", 1024);
    vi.stubGlobal("innerHeight", 768);
    const start = vi.fn<
      (callback: () => void) => { ready: Promise<void>; skipTransition: () => void }
    >((callback) => {
      callback();
      return { ready: Promise.resolve(), skipTransition };
    });
    Object.defineProperty(document, "startViewTransition", { configurable: true, value: start });
    Object.defineProperty(document.documentElement, "animate", {
      configurable: true,
      value: animate,
    });
    revealTheme(button, false, update);
    await Promise.resolve();
    expect(animate).toHaveBeenCalledWith(
      { clipPath: ["circle(0px at 0px 0px)", "circle(1280px at 0px 0px)"] },
      {
        pseudoElement: "::view-transition-new(root)",
        duration: 420,
        easing: "cubic-bezier(0.22, 1, 0.36, 1)",
      },
    );
    reduced = true;
    revealTheme(button, false, update);
    await Promise.resolve();
    expect(skipTransition).toHaveBeenCalledOnce();
    start.mockImplementationOnce((callback) => {
      callback();
      return { ready: Promise.reject(new Error("snapshot skipped")), skipTransition };
    });
    reduced = false;
    revealTheme(button, false, update);
    await Promise.resolve();
    expect(update).toHaveBeenCalledTimes(3);
    expect(animate).toHaveBeenCalledOnce();
    revealTheme(button, true, update);
    expect(update).toHaveBeenCalledTimes(4);
    Reflect.deleteProperty(document, "startViewTransition");
    Reflect.deleteProperty(document.documentElement, "animate");
    revealTheme(button, false, update);
    expect(update).toHaveBeenCalledTimes(5);
  });
});
