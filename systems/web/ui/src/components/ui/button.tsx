import type { ComponentProps } from "react";
import { clsx } from "clsx";
import styles from "./button.module.css";

// Native button semantics with shared Catppuccin theme tokens.
export function Button({
  className,
  variant = "default",
  size = "default",
  type = "button",
  ...props
}: ComponentProps<"button"> & {
  variant?: "default" | "ghost" | "outline";
  size?: "default" | "sm" | "icon";
}) {
  return (
    <button
      data-slot="button"
      type={type}
      className={clsx(styles.button, styles[variant], styles[`size${size}`], className)}
      {...props}
    />
  );
}
