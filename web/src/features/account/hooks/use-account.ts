"use client";

import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";
import { useEffect, useState, type FormEvent } from "react";

import { useGatewayUser } from "@/components/setup-gate";
import type { GatewaySession } from "@/features/auth/types/auth.types";
import { GatewayAPIError } from "@/lib/pocket-ai-gateway-admin.client";

export function useAccount() {
  const user = useGatewayUser();
  const [sessions, setSessions] = useState<GatewaySession[]>([]);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  function showError(failure: unknown) { setError(failure instanceof GatewayAPIError ? failure.message : "Request failed"); }
  useEffect(() => { void pocketAIGatewayAdmin.account.sessions().then(({ items }) => setSessions(items)).catch(showError); }, []);

  async function updateProfile(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setError(""); setMessage("");
    const form = new FormData(event.currentTarget);
    try {
      await pocketAIGatewayAdmin.account.updateProfile({ email: String(form.get("email")), display_name: String(form.get("display_name")), current_password: String(form.get("current_password")) });
      window.location.reload();
    } catch (failure) { showError(failure); }
  }

  async function changePassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setError(""); setMessage("");
    const form = new FormData(event.currentTarget);
    try {
      await pocketAIGatewayAdmin.account.changePassword({ current_password: String(form.get("current_password")), new_password: String(form.get("new_password")) });
      event.currentTarget.reset(); setMessage("Password changed. Other sessions were signed out.");
      setSessions((await pocketAIGatewayAdmin.account.sessions()).items);
    } catch (failure) { showError(failure); }
  }

  async function logout() { setError(""); try { await pocketAIGatewayAdmin.account.logout(); window.location.replace("/_/login/"); } catch (failure) { showError(failure); } }
  async function revoke(id: string, current: boolean) {
    try { await pocketAIGatewayAdmin.account.revokeSession(id); if (current) window.location.replace("/_/login/"); else setSessions((items) => items.filter((item) => item.id !== id)); } catch (failure) { showError(failure); }
  }

  return { user, sessions, message, error, updateProfile, changePassword, logout, revoke };
}
