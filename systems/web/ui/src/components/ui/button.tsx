import type { HTMLMotionProps } from "motion/react";

import { clsx } from "clsx";
import * as motion from "motion/react-m";

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
      {...(!reduced && props.disabled !== true
        ? {
            whileHover: { scale: 1.04 },
            whileTap: { scale: 0.96 },
          }
        : {})}
      transition={{
        default: reduced ? { duration: 0 } : { type: "spring", stiffness: 500, damping: 32 },
      }}
      {...props}
    />
  );
}
