import type { Metadata } from "next";
import type { ReactNode } from "react";

import { SetupGate } from "@/components/setup-gate";

import "./styles.css";

export const metadata: Metadata = {
  title: {
    default: "Pocket AI Gateway",
    template: "%s · Pocket AI Gateway",
  },
  description: "Local administration for Pocket AI Gateway.",
};

export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="en">
      <body>
        <a className="skip-link" href="#main-content">
          Skip to content
        </a>
        <SetupGate>{children}</SetupGate>
      </body>
    </html>
  );
}
