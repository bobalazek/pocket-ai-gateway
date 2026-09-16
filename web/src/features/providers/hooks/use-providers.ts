"use client";

import { useEffect, useState, type FormEvent } from "react";

import { useGatewayUser } from "@/components/setup-gate";
import type { ProviderAdapter, ProviderConnection, ProviderPreset } from "@/features/providers/types/providers.types";
import { GatewayAPIError, pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

export function useProviders() {
  const user = useGatewayUser();
  const [items, setItems] = useState<ProviderConnection[]>([]);
  const [presets, setPresets] = useState<ProviderPreset[]>([]);
  const [adapters, setAdapters] = useState<ProviderAdapter[]>([]);
  const [selectedPreset, setSelectedPreset] = useState("");
  const [adapter, setAdapter] = useState<ProviderConnection["adapter"]>("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const selected = presets.find((preset) => preset.id === selectedPreset);
  const fail = (failure: unknown, fallback: string) => setError(failure instanceof GatewayAPIError ? failure.message : fallback);
  const defaultAdapter = (options = adapters) => options.find((item) => item.default)?.id ?? options[0]?.id ?? "";

  async function load() {
    try {
      const [connections, availablePresets] = await Promise.all([
        pocketAIGatewayAdmin.providers.connections(),
        pocketAIGatewayAdmin.providers.presets(),
      ]);
      setItems(connections.data);
      setPresets(availablePresets.data);
      setAdapters(availablePresets.adapters);
      setAdapter((current) => current || defaultAdapter(availablePresets.adapters));
      setError("");
    } catch (failure) {
      fail(failure, "Providers are unavailable");
    }
  }

  useEffect(() => { void load(); }, []);

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const element = event.currentTarget;
    const form = new FormData(element);
    setBusy(true);
    try {
      await pocketAIGatewayAdmin.providers.createConnection({ name: String(form.get("name")), preset: String(form.get("preset")), adapter: String(form.get("adapter")) as ProviderConnection["adapter"], base_url: String(form.get("base_url")), enabled: true, allow_private_network: form.get("allow_private_network") === "on", timeout_ms: Number(form.get("timeout_ms")) });
      element.reset();
      setSelectedPreset("");
      setAdapter(defaultAdapter());
      await load();
    } catch (failure) { fail(failure, "Connection could not be created"); } finally { setBusy(false); }
  }

  async function credential(event: FormEvent<HTMLFormElement>, id: string) {
    event.preventDefault();
    const element = event.currentTarget;
    const form = new FormData(element);
    if (!window.confirm("Replace this provider credential? Existing requests keep their recorded provenance.")) return;
    setBusy(true);
    try {
      const mode = String(form.get("mode"));
      await pocketAIGatewayAdmin.providers.putCredential(id, mode === "external" ? { external_ref: String(form.get("value")) } : { credential: String(form.get("value")) });
      element.reset();
      await load();
    } catch (failure) { fail(failure, "Credential could not be saved"); } finally { setBusy(false); }
  }

  async function addModel(event: FormEvent<HTMLFormElement>, id: string) {
    event.preventDefault();
    const element = event.currentTarget;
    const form = new FormData(element);
    setBusy(true);
    try {
      await pocketAIGatewayAdmin.providers.createUpstreamModel(id, { upstream_id: String(form.get("upstream_id")), capabilities: form.getAll("capabilities").map(String) });
      element.reset();
    } catch (failure) { fail(failure, "Upstream model could not be added"); } finally { setBusy(false); }
  }

  async function toggle(item: ProviderConnection) {
    if (!window.confirm(`${item.enabled ? "Disable" : "Enable"} ${item.name}?`)) return;
    setBusy(true);
    try {
      await pocketAIGatewayAdmin.providers.updateConnection(item, { name: item.name, preset: item.preset, adapter: item.adapter, base_url: item.base_url, enabled: !item.enabled, allow_private_network: item.allow_private_network, timeout_ms: item.timeout_ms });
      await load();
    } catch (failure) { fail(failure, "Connection could not be updated"); } finally { setBusy(false); }
  }

  function choosePreset(value: string) {
    const preset = value === "custom" ? "" : value;
    setSelectedPreset(preset);
    setAdapter(presets.find((item) => item.id === preset)?.adapter ?? defaultAdapter());
  }

  return { user, items, presets, adapters, selectedPreset, adapter, setAdapter, selected, error, busy, create, credential, addModel, toggle, choosePreset };
}
