"use client";

import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";
import { useState, type FormEvent } from "react";

import { GatewayAPIError } from "@/lib/pocket-ai-gateway-admin.client";

export function useSetup() {
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    const form = new FormData(event.currentTarget);
    try {
      await pocketAIGatewayAdmin.auth.claimOwner({
        display_name: String(form.get("display_name") ?? ""),
        email: String(form.get("email") ?? ""),
        password: String(form.get("password") ?? ""),
      });
      window.location.replace("/_/providers/");
    } catch (failure) {
      if (failure instanceof GatewayAPIError && failure.code === "setup_complete") {
        window.location.replace("/_/");
        return;
      }
      setError(failure instanceof GatewayAPIError ? failure.message : "Setup could not be completed");
      setSubmitting(false);
    }
  }

  return { error, submitting, submit };
}
