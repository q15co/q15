import type { HTMLMotionProps } from "motion/react";

import { clsx } from "clsx";
import * as motion from "motion/react-m";

import { recursiveAxes } from "./font-motion";
import { useMotionPreference } from "./motion-preference";

import styles from "./button.module.css";

// Native button semantics with shared Catppuccin theme tokens.
export function Button({
  className,
  variant = "default",
  size = "default",
  type = "button",
  ...props
}: HTMLMotionProps<"button"> & {
  variant?: "default" | "ghost" | "outline";
  size?: "default" | "sm" | "icon";
}) {
  const reduced = useMotionPreference();
  return (
    <motion.button
      type={type}
      className={clsx(styles.button, styles[variant], styles[`size${size}`], className)}
      initial={false}
      animate={{ fontVariationSettings: recursiveAxes(0.2, 500) }}
      {...(!reduced && props.disabled !== true
        ? {
            whileHover: { scale: 1.04, fontVariationSettings: recursiveAxes(0.9, 550, -3) },
            whileFocus: { fontVariationSettings: recursiveAxes(0.9, 550, -3) },
            whileTap: { scale: 0.96 },
          }
        : {})}
      transition={{
        default: reduced ? { duration: 0 } : { type: "spring", stiffness: 500, damping: 32 },
        fontVariationSettings: { duration: reduced ? 0 : 0.32, ease: "easeOut" },
      }}
      {...props}
    />
  );
}
