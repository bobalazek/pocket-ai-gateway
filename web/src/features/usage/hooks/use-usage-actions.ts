"use client";

import { useRef, useState, type FormEvent } from "react";

import type { LimitPolicy, PricePreview, RepricePreview, AdjustmentPreview, ReconciliationPreview, UnresolvedAttempt } from "@/features/usage/types/usage.types";
import { parseWeeklyPriceWindow } from "@/features/usage/utils/pricing.utils";
import { datetime, failureText, policyKinds } from "@/features/usage/utils/usage.utils";
import { pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

export function useUsageActions(reload: () => Promise<void>) {
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  const [reprice, setReprice] = useState<RepricePreview | null>(null);
  const [pricePreview, setPricePreview] = useState<PricePreview | null>(null);
  const [adjustment, setAdjustment] = useState<AdjustmentPreview | null>(null);
  const [reconciliation, setReconciliation] = useState<ReconciliationPreview | null>(null);
  const fail = (failure: unknown, fallback: string) => setError(failureText(failure, fallback));

  async function createPolicy(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const element = event.currentTarget;
    const form = new FormData(element);
    const [metric, algorithm] = policyKinds[String(form.get("kind")) as keyof typeof policyKinds];
    const limit = String(form.get("limit"));
    const limitUnits = metric === "spend" ? 1 : Number(limit);
    setBusy(true);
    try {
      if (!Number.isSafeInteger(limitUnits) || limitUnits < 1) throw new Error("Non-spend limits must be positive whole numbers");
      await pocketAIGatewayAdmin.usage.createPolicy({ scope_kind: String(form.get("scope_kind")) as LimitPolicy["scope_kind"], scope_id: String(form.get("scope_id")), metric, algorithm, period: algorithm === "quota" ? String(form.get("period")) as LimitPolicy["period"] : "", window_seconds: algorithm === "fixed_window" ? Number(form.get("window_seconds")) : 0, limit_units: limitUnits, limit_usd: metric === "spend" ? limit : "", refill_units: algorithm === "token_bucket" ? Number(form.get("refill_units")) : 0, refill_interval_ms: algorithm === "token_bucket" ? Number(form.get("refill_interval_ms")) : 0, enabled: true });
      element.reset();
      await reload();
    } catch (failure) { fail(failure, "Policy could not be created"); } finally { setBusy(false); }
  }

  async function changePolicy(policy: LimitPolicy, value: string, enabled: boolean) {
    if (!window.confirm(`${enabled === policy.enabled ? "Change" : enabled ? "Enable" : "Disable"} ${policy.metric} policy${enabled === policy.enabled ? ` to ${value}` : ""}?`)) return;
    setBusy(true);
    try {
      await pocketAIGatewayAdmin.usage.updatePolicy(policy, { limit_units: policy.metric === "spend" ? 1 : Number(value), limit_usd: value, enabled });
      await reload();
    } catch (failure) { fail(failure, "Policy could not be updated"); } finally { setBusy(false); }
  }

  async function createPrice(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const element = event.currentTarget;
    if (!pricePreview) {
      const form = new FormData(element);
      try {
        const weeklyWindow = parseWeeklyPriceWindow(String(form.get("weekly_start_day") ?? ""), String(form.get("weekly_start_time") ?? ""), String(form.get("weekly_end_day") ?? ""), String(form.get("weekly_end_time") ?? ""));
        setPricePreview({ connection_id: String(form.get("connection_id")), model_id: String(form.get("model_id")), input_usd_per_million: String(form.get("input_price")), cache_read_usd_per_million: String(form.get("cache_read_price") ?? "").trim() || null, output_usd_per_million: String(form.get("output_price")), source: String(form.get("source")), effective_from: datetime(form.get("effective_from")), effective_to: datetime(form.get("effective_to")), ...weeklyWindow });
        setError("");
      } catch (failure) {
        setError(failure instanceof Error ? failure.message : "Weekly price window is invalid");
      }
      return;
    }
    setBusy(true);
    try { await pocketAIGatewayAdmin.usage.createPrice(pricePreview); setPricePreview(null); element.reset(); await reload(); }
    catch (failure) { fail(failure, "Price could not be created"); } finally { setBusy(false); }
  }

  async function previewReprice(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const input = { connection_id: String(form.get("connection_id")), model_id: String(form.get("model_id")), from: datetime(form.get("from")), to: datetime(form.get("to")) };
    setBusy(true);
    try {
      const result = await pocketAIGatewayAdmin.usage.previewReprice(input);
      setReprice({ input, key: crypto.randomUUID(), text: `${input.connection_id} / ${input.model_id}, ${new Date(input.from).toLocaleString()}–${new Date(input.to).toLocaleString()}: ${result.preview.affected_attempts} attempts, ${result.preview.missing_prices} missing, $${result.preview.delta_usd} change` });
    } catch (failure) { fail(failure, "Preview failed"); } finally { setBusy(false); }
  }

  async function applyReprice(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!reprice || busyRef.current) return;
    busyRef.current = true; setBusy(true);
    try { await pocketAIGatewayAdmin.usage.applyReprice({ ...reprice.input, idempotency_key: reprice.key }); setReprice(null); setNotice("Repricing applied."); await reload(); }
    catch (failure) { fail(failure, "Repricing failed"); } finally { busyRef.current = false; setBusy(false); }
  }

  async function adjust(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busyRef.current) return;
    if (!adjustment) {
      const form = new FormData(event.currentTarget);
      setAdjustment({ attempt_id: String(form.get("attempt_id")), delta_usd: String(form.get("delta_usd")), reason: String(form.get("reason")), idempotency_key: crypto.randomUUID() });
      return;
    }
    busyRef.current = true; setBusy(true);
    try { await pocketAIGatewayAdmin.usage.adjust(adjustment); setAdjustment(null); setNotice("Cost adjusted."); event.currentTarget.reset(); await reload(); }
    catch (failure) { fail(failure, "Adjustment failed"); } finally { busyRef.current = false; setBusy(false); }
  }

  async function reconcile(event: FormEvent<HTMLFormElement>, attempt: UnresolvedAttempt) {
    event.preventDefault();
    if (!reconciliation || reconciliation.attempt_id !== attempt.id) {
      const form = new FormData(event.currentTarget);
      setReconciliation({ attempt_id: attempt.id, input_tokens: Number(form.get("input_tokens")), output_tokens: Number(form.get("output_tokens")), cost_usd: String(form.get("cost_usd")) || undefined, usage_status: String(form.get("usage_status")) as "provider_reported" | "estimated", reason: String(form.get("reason")), idempotency_key: crypto.randomUUID() });
      return;
    }
    if (busyRef.current) return;
    busyRef.current = true; setBusy(true);
    try { await pocketAIGatewayAdmin.usage.reconcile(reconciliation); setReconciliation(null); setNotice("Usage reconciled."); await reload(); }
    catch (failure) { fail(failure, "Reconciliation failed"); } finally { busyRef.current = false; setBusy(false); }
  }

  return { error, notice, busy, reprice, pricePreview, adjustment, reconciliation, createPolicy, changePolicy, createPrice, previewReprice, applyReprice, adjust, reconcile, setPricePreview, setReprice, setAdjustment, setReconciliation };
}
