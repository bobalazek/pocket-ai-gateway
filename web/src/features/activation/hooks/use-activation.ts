"use client";

import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";
import { useState, type FormEvent } from "react";

import { GatewayAPIError } from "@/lib/pocket-ai-gateway-admin.client";

export function useActivation() {
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    const form = new FormData(event.currentTarget);
    try {
      await pocketAIGatewayAdmin.auth.activate({ code: String(form.get("code") ?? ""), password: String(form.get("password") ?? "") });
      window.location.replace("/_/");
    } catch (failure) {
      setError(failure instanceof GatewayAPIError ? failure.message : "Activation could not be completed");
      setSubmitting(false);
    }
  }

  return { error, submitting, submit };
}
