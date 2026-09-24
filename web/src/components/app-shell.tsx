"use client";

import Link from "next/link";
import { ActivityIcon, BoxesIcon, ChartNoAxesCombinedIcon, ChevronUpIcon, CircleHelpIcon, FileClockIcon, FilmIcon, FlaskConicalIcon, KeyRoundIcon, LayoutDashboardIcon, LogOutIcon, MenuIcon, ScrollTextIcon, ServerIcon, SettingsIcon, UserRoundIcon, UsersIcon, WalletIcon, XIcon } from "lucide-react";
import type { ReactNode } from "react";

import { useGatewayUser } from "@/components/setup-gate";
import { useAppShell } from "@/hooks/use-app-shell";

export type AppSection = "Overview" | "Status" | "Users" | "Providers" | "Models" | "Media jobs" | "API keys" | "Playground" | "Requests" | "Analytics" | "Usage" | "Audit" | "Settings" | "Account";
type NavItem = { label: AppSection; href: string; icon: typeof LayoutDashboardIcon; admin?: boolean; owner?: boolean };

const primary: NavItem[] = [
  { label: "Overview", href: "/", icon: LayoutDashboardIcon },
  { label: "Users", href: "/users/", icon: UsersIcon, admin: true },
  { label: "Providers", href: "/providers/", icon: ServerIcon, admin: true },
  { label: "Models", href: "/models/", icon: BoxesIcon },
  { label: "Media jobs", href: "/media-jobs/", icon: FilmIcon, admin: true },
  { label: "API keys", href: "/keys/", icon: KeyRoundIcon },
  { label: "Requests", href: "/requests/", icon: FileClockIcon },
  { label: "Analytics", href: "/analytics/", icon: ChartNoAxesCombinedIcon },
  { label: "Usage", href: "/usage/", icon: WalletIcon },
  { label: "Audit", href: "/audit/", icon: ScrollTextIcon, admin: true },
  { label: "Settings", href: "/settings/", icon: SettingsIcon, owner: true },
];
const tools: NavItem[] = [
  { label: "Playground", href: "/playground/", icon: FlaskConicalIcon },
  { label: "Status", href: "/status/", icon: ActivityIcon },
];

function Navigation({ active, onNavigate }: { active: AppSection; onNavigate?: () => void }) {
  const user = useGatewayUser();
  const allowed = (item: NavItem) => (!item.admin || user?.role === "owner" || user?.role === "admin") && (!item.owner || user?.role === "owner");
  const links = (items: NavItem[]) => items.filter(allowed).map(({ label, href, icon: Icon }) => (
    <Link className={`nav-item${active === label ? " active" : ""}`} href={href} key={label} aria-current={active === label ? "page" : undefined} onClick={onNavigate}>
      <Icon aria-hidden="true" size={17} /><span className="nav-label">{label}</span>
    </Link>
  ));
  return <nav aria-label="Gateway sections"><div>{links(primary)}</div><div className="nav-tools"><p>Tools</p>{links(tools)}</div></nav>;
}

function AccountMenu({ active, open, error, version, onToggle, onClose, onLogout }: { active: AppSection; open: boolean; error: string; version: string; onToggle: () => void; onClose: () => void; onLogout: () => void }) {
  const user = useGatewayUser();
  return <div className="account-menu">
    {open && <nav className="account-popover" aria-label="Account links">
      <div className="account-identity"><strong>{user?.display_name ?? "Account"}</strong><small>{user?.email}</small>{version && <small>Version {version}</small>}<span>{user?.role ?? "signed out"}</span></div>
      <Link href="/account/" onClick={onClose}><UserRoundIcon aria-hidden="true" size={16}/>Account and security</Link>
      <Link href="/status/" onClick={onClose}><CircleHelpIcon aria-hidden="true" size={16}/>Help and runtime status</Link>
      <button type="button" onClick={onLogout}><LogOutIcon aria-hidden="true" size={16}/>Sign out</button>
      {error && <p className="account-error" role="alert">{error}</p>}
    </nav>}
    <button type="button" className={`account-summary${active === "Account" ? " active" : ""}`} aria-expanded={open} aria-label={`${open ? "Close" : "Open"} account links for ${user?.display_name ?? "account"}`} onClick={onToggle}>
      <span className="account-avatar" aria-hidden="true">{user?.display_name.trim().charAt(0).toUpperCase() || "U"}</span>
      <span><strong>{user?.display_name ?? "Account"}</strong>{user && <small>{user.email}</small>}</span>
      <ChevronUpIcon aria-hidden="true" size={16} />
    </button>
  </div>;
}

export function AppShell({ active, children }: { active: AppSection; children: ReactNode }) {
  const user = useGatewayUser();
  const shell = useAppShell(user?.role);
  const closeMobile = () => { shell.setMobileOpen(false); shell.mobileButton.current?.focus(); };
  return <div className="app-shell">
    <aside className="sidebar" aria-label="Gateway navigation">
      <div className="brand"><span className="brand-mark" aria-hidden="true">P</span><span><strong>Pocket AI</strong><small>Gateway</small></span></div>
      <div className="desktop-navigation"><Navigation active={active} /></div>
      <div className="sidebar-footer"><AccountMenu active={active} open={shell.accountOpen} error={shell.accountError} version={shell.version} onToggle={() => shell.setAccountOpen((value) => !value)} onClose={() => shell.setAccountOpen(false)} onLogout={() => void shell.logout()} /></div>
      <button ref={shell.mobileButton} className="mobile-menu-button" type="button" aria-expanded={shell.mobileOpen} aria-controls="mobile-navigation" onClick={() => shell.setMobileOpen((value) => !value)}>
        {shell.mobileOpen ? <XIcon aria-hidden="true" size={20}/> : <MenuIcon aria-hidden="true" size={20}/>}<span className="sr-only">{shell.mobileOpen ? "Close navigation" : "Open navigation"}</span>
      </button>
    </aside>
    {shell.mobileOpen && <div className="mobile-menu-backdrop" onMouseDown={closeMobile}><div id="mobile-navigation" ref={shell.drawer} className="mobile-menu-panel" role="dialog" aria-modal="true" aria-label="Gateway navigation" onMouseDown={(event) => event.stopPropagation()}><div className="mobile-menu-heading"><strong>Navigation</strong><button type="button" onClick={closeMobile} aria-label="Close navigation"><XIcon aria-hidden="true" size={20}/></button></div><Navigation active={active} onNavigate={closeMobile}/><AccountMenu active={active} open={shell.accountOpen} error={shell.accountError} version={shell.version} onToggle={() => shell.setAccountOpen((value) => !value)} onClose={closeMobile} onLogout={() => void shell.logout()}/></div></div>}
    {children}
  </div>;
}
