import * as m from "motion/react-m";
import type { HTMLMotionProps } from "motion/react";
import { clsx } from "clsx";
import styles from "./button.module.css";
import { useMotionPreference } from "./motion";

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
      data-slot="button"
      type={type}
      className={clsx(styles.button, styles[variant], styles[`size${size}`], className)}
      whileHover={!reduced && !props.disabled ? { scale: 1.04 } : undefined}
      whileTap={!reduced && !props.disabled ? { scale: 0.96 } : undefined}
      transition={reduced ? { duration: 0 } : { type: "spring", stiffness: 500, damping: 32 }}
      {...props}
    />
  );
}
