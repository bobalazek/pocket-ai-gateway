import Link from "next/link";

import { buttonVariants } from "@/components/ui/button";

export default function NotFound() {
  return (
    <main id="main-content" className="not-found">
      <p className="error-code">404</p>
      <h1>Page not found</h1>
      <p>This dashboard page does not exist.</p>
      <Link className={buttonVariants()} href="/">Return to overview</Link>
    </main>
  );
}
