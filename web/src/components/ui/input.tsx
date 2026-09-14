import type { InputHTMLAttributes } from "react";

import { cn } from "@/lib/utils";

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input className={cn("min-h-11 w-full rounded-lg border border-[var(--input-border)] bg-white px-3 py-2 text-[var(--text)] outline-none focus-visible:outline-3 focus-visible:outline-offset-2 focus-visible:outline-[var(--focus)]", className)} {...props} />;
}
