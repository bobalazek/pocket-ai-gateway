"use client";

import { useEffect, useState, type FormEvent } from "react";

import { useGatewayUser } from "@/components/setup-gate";
import type { ProviderAdapter, ProviderAdapterScript, ProviderConnection, ProviderPreset, UpstreamModel } from "@/features/providers/types/providers.types";
import { GatewayAPIError, pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

export function useProviders() {
  const user = useGatewayUser();
  const [items, setItems] = useState<ProviderConnection[]>([]);
  const [models, setModels] = useState<Record<string, UpstreamModel[] | null>>({});
  const [presets, setPresets] = useState<ProviderPreset[]>([]);
  const [adapters, setAdapters] = useState<ProviderAdapter[]>([]);
  const [scripts, setScripts] = useState<Record<string, ProviderAdapterScript | null>>({});
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
      const lists = await Promise.all(connections.data.map(async (connection) => {
        try {
          const result = await pocketAIGatewayAdmin.providers.upstreamModels(connection.id);
          return [connection.id, result.data] as const;
        } catch {
          return [connection.id, null] as const;
        }
      }));
      setModels(Object.fromEntries(lists));
      setError(lists.some(([, list]) => list === null) ? "Some upstream model lists could not be loaded" : "");
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
      try {
        const result = await pocketAIGatewayAdmin.providers.upstreamModels(id);
        setModels((current) => ({ ...current, [id]: result.data }));
        setError("");
      } catch {
        setModels((current) => ({ ...current, [id]: null }));
        setError("Model added, but its list could not be refreshed");
      }
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

  async function loadScript(item: ProviderConnection) {
    if (scripts[item.id] !== undefined) return;
    try {
      const result = await pocketAIGatewayAdmin.providers.adapterScript(item.id);
      const script = result.adapter_script;
      setScripts((current) => ({ ...current, [item.id]: script.request_script || script.response_script ? script : null }));
    } catch (failure) {
      if (failure instanceof GatewayAPIError && failure.status === 404) {
        setScripts((current) => ({ ...current, [item.id]: null }));
        return;
      }
      fail(failure, "Adapter script could not be loaded");
    }
  }

  async function saveScript(event: FormEvent<HTMLFormElement>, item: ProviderConnection) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    setBusy(true);
    try {
      const result = await pocketAIGatewayAdmin.providers.putAdapterScript(item, {
        request_script: String(form.get("request_script")),
        response_script: String(form.get("response_script")),
      });
      setScripts((current) => ({ ...current, [item.id]: result.adapter_script }));
      setError("");
      await load();
    } catch (failure) { fail(failure, "Adapter script could not be saved"); } finally { setBusy(false); }
  }

  async function removeScript(item: ProviderConnection) {
    if (!window.confirm(`Remove the adapter script from ${item.name}?`)) return;
    setBusy(true);
    try {
      await pocketAIGatewayAdmin.providers.deleteAdapterScript(item);
      setScripts((current) => ({ ...current, [item.id]: null }));
      setError("");
      await load();
    } catch (failure) { fail(failure, "Adapter script could not be removed"); } finally { setBusy(false); }
  }

  function choosePreset(value: string) {
    const preset = value === "custom" ? "" : value;
    setSelectedPreset(preset);
    setAdapter(presets.find((item) => item.id === preset)?.adapter ?? defaultAdapter());
  }

  return { user, items, models, presets, adapters, scripts, selectedPreset, adapter, setAdapter, selected, error, busy, create, credential, addModel, toggle, choosePreset, loadScript, saveScript, removeScript };
}
