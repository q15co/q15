import { createContext, useContext, useSyncExternalStore } from "react";
import type { ReactNode } from "react";
import { LazyMotion, MotionConfig } from "motion/react";

const ReducedMotion = createContext(true);
const query = "(prefers-reduced-motion: reduce)";
const loadFeatures = () => import("./motion-features").then((module) => module.default);
function subscribe(listener: () => void) {
  const preference = window.matchMedia(query);
  preference.addEventListener("change", listener);
  return () => preference.removeEventListener("change", listener);
}
export const useMotionPreference = () => useContext(ReducedMotion);

export function MotionProvider({ children }: { children: ReactNode }) {
  const reduced = useSyncExternalStore(
    subscribe,
    () => window.matchMedia(query).matches,
    () => true,
  );
  return (
    <ReducedMotion value={reduced}>
      <LazyMotion features={loadFeatures} strict>
        <MotionConfig
          reducedMotion={reduced ? "always" : "never"}
          transition={{ duration: reduced ? 0 : 0.24, ease: [0.22, 1, 0.36, 1] }}
        >
          {children}
        </MotionConfig>
      </LazyMotion>
    </ReducedMotion>
  );
}
