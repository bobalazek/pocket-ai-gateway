"use client";

import Link from "next/link";
import { ActivityIcon, KeyRoundIcon, LayoutDashboardIcon, MenuIcon, UserRoundIcon, UsersIcon, XIcon } from "lucide-react";
import type { ReactNode } from "react";

import { useGatewayUser } from "@/components/setup-gate";

export type AppSection = "Overview" | "Status" | "Users" | "API keys" | "Account";

const navigation: { label: string; href?: string; icon?: typeof LayoutDashboardIcon; admin?: boolean }[] = [
  { label: "Overview", href: "/", icon: LayoutDashboardIcon },
  { label: "Status", href: "/status/", icon: ActivityIcon },
  { label: "Users", href: "/users/", icon: UsersIcon, admin: true },
	{ label: "Providers", admin: true },
	{ label: "Models" },
  { label: "API keys", href: "/keys/", icon: KeyRoundIcon },
	{ label: "Requests" },
	{ label: "Usage" },
	{ label: "Audit", admin: true },
	{ label: "Settings", admin: true },
];

function Navigation({ active }: { active: AppSection }) {
  const user = useGatewayUser();
  return (
    <nav>
		{navigation.filter((item) => !item.admin || user?.role === "owner" || user?.role === "admin").map(({ label, href, icon: Icon }) => href ? (
			<Link className={`nav-item${active === label ? " active" : ""}`} href={href} key={label}>
				{Icon && <Icon aria-hidden="true" size={17} />}{label}
			</Link>
		) : <span className="nav-item disabled" aria-disabled="true" key={label}>{label}</span>)}
    </nav>
  );
}

function AccountSummary({ active }: { active: AppSection }) {
  const user = useGatewayUser();
  return (
    <Link className={`account-summary${active === "Account" ? " active" : ""}`} href="/account/" aria-label="Open account settings">
      <span className="account-avatar" aria-hidden="true">{user?.display_name.trim().charAt(0).toUpperCase() || "U"}</span>
      <span><strong>{user?.display_name ?? "Account"}</strong>{user && <small>{user.email}</small>}</span>
      <UserRoundIcon aria-hidden="true" size={16} />
    </Link>
  );
}

export function AppShell({ active, children }: { active: AppSection; children: ReactNode }) {
  return (
    <div className="app-shell">
      <aside className="sidebar" aria-label="Gateway navigation">
        <div className="brand"><span className="brand-mark" aria-hidden="true">P</span><span><strong>Pocket AI</strong><small>Gateway</small></span></div>
        <div className="desktop-navigation"><Navigation active={active} /></div>
        <div className="sidebar-footer"><AccountSummary active={active} /></div>
        <details className="mobile-menu">
          <summary aria-label="Toggle navigation">
            <MenuIcon className="menu-open-icon" aria-hidden="true" size={20} />
            <XIcon className="menu-close-icon" aria-hidden="true" size={20} />
            <span className="sr-only">Toggle navigation</span>
          </summary>
          <div className="mobile-menu-panel"><Navigation active={active} /><AccountSummary active={active} /></div>
        </details>
      </aside>
      {children}
    </div>
  );
}
