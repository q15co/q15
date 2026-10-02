import * as m from "motion/react-m";
import type { HTMLMotionProps } from "motion/react";
import { clsx } from "clsx";
import styles from "./button.module.css";
import { useMotionPreference } from "./motion";
import { recursiveAxes } from "./font-motion";

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
    <m.button
      type={type}
      className={clsx(styles.button, styles[variant], styles[`size${size}`], className)}
      initial={false}
      animate={{ fontVariationSettings: recursiveAxes(0.2, 500) }}
      whileHover={
        !reduced && !props.disabled
          ? { scale: 1.04, fontVariationSettings: recursiveAxes(0.9, 550, -3) }
          : undefined
      }
      whileFocus={
        !reduced && !props.disabled
          ? { fontVariationSettings: recursiveAxes(0.9, 550, -3) }
          : undefined
      }
      whileTap={!reduced && !props.disabled ? { scale: 0.96 } : undefined}
      transition={{
        default: reduced ? { duration: 0 } : { type: "spring", stiffness: 500, damping: 32 },
        fontVariationSettings: { duration: reduced ? 0 : 0.32, ease: "easeOut" },
      }}
      {...props}
    />
  );
}
