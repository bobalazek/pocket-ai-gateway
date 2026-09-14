"use client";

import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

export function ChartContainer({ label, className, children }: { label: string; className?: string; children: ReactNode }) {
  return <div className={cn("chart-container", className)} role="img" aria-label={label}>{children}</div>;
}
