import type { InputHTMLAttributes, ReactNode } from "react";

import { cn } from "@/lib/utils";

type SwitchProps = Omit<InputHTMLAttributes<HTMLInputElement>, "type" | "role"> & { label: ReactNode; description?: ReactNode };

/** An on/off setting. A native checkbox keeps form submission, keyboard support, and the checked state for assistive technology. */
export function Switch({ label, description, className, ...props }: SwitchProps) {
  return (
    <label className={cn("switch-row", className)}>
      <input type="checkbox" role="switch" className="switch" {...props} />
      <span className="switch-text">
        <span>{label}</span>
        {description && <small>{description}</small>}
      </span>
    </label>
  );
}
