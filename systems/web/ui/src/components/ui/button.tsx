import type { ComponentProps } from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { clsx } from "clsx";
import { twMerge } from "tailwind-merge";

// shadcn/ui button, with native button semantics and Catppuccin theme tokens.
const variants = cva(
  "inline-flex shrink-0 items-center justify-center gap-2 rounded-xl text-sm font-medium whitespace-nowrap transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-40 [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground hover:bg-primary/85",
        ghost: "text-muted-foreground hover:bg-accent hover:text-foreground",
        outline: "border border-border bg-background hover:bg-accent",
      },
      size: { default: "h-10 px-4 py-2", sm: "h-8 px-3 text-xs", icon: "size-10" },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);
export function Button({
  className,
  variant,
  size,
  type = "button",
  ...props
}: ComponentProps<"button"> & VariantProps<typeof variants>) {
  return (
    <button
      data-slot="button"
      type={type}
      className={twMerge(clsx(variants({ variant, size }), className))}
      {...props}
    />
  );
}
