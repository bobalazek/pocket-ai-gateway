"use client";

import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";
import { useEffect, useState, type FormEvent } from "react";

import { GatewayAPIError } from "@/lib/pocket-ai-gateway-admin.client";

export function useLogin() {
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [expired, setExpired] = useState(false);

  useEffect(() => setExpired(new URLSearchParams(window.location.search).get("reason") === "session-expired"), []);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    const form = new FormData(event.currentTarget);
    try {
      await pocketAIGatewayAdmin.auth.login({ email: String(form.get("email") ?? ""), password: String(form.get("password") ?? "") });
      window.location.replace("/_/");
    } catch (failure) {
      setError(failure instanceof GatewayAPIError ? failure.message : "Login could not be completed");
      setSubmitting(false);
    }
  }

  return { error, expired, submitting, submit };
}
