"use client";

import { useState, type Dispatch, type FormEvent, type SetStateAction } from "react";

import type { CatalogState, PublicModel, RoutePlan, RoutePreviewInput, RouteStrategy } from "@/features/models/types/models.types";
import { GatewayAPIError, pocketAIGatewayAdmin } from "@/lib/pocket-ai-gateway-admin.client";

const failureText = (error: unknown, fallback: string) => error instanceof GatewayAPIError ? error.message : fallback;

type Options = {
  reload: () => Promise<void>;
  setCatalogState: Dispatch<SetStateAction<CatalogState | null>>;
  setSelectedTarget: Dispatch<SetStateAction<string>>;
};

export function useModelsActions({ reload, setCatalogState, setSelectedTarget }: Options) {
  const [previews, setPreviews] = useState<Record<string, { route: RoutePlan; input: RoutePreviewInput }>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function publishModel(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const element = event.currentTarget;
    const form = new FormData(element);
    setError("");
    setBusy(true);
    try {
      await pocketAIGatewayAdmin.models.createPublicModel({
        id: String(form.get("id")),
        label: String(form.get("label")),
        description: String(form.get("description")),
        target_model_id: String(form.get("target_model_id")),
        capabilities: form.getAll("capabilities").map(String),
      });
      element.reset();
      setSelectedTarget("");
      await reload();
    } catch (failure) {
      setError(failureText(failure, "Model could not be published"));
    } finally {
      setBusy(false);
    }
  }

  async function updateModelRoute(event: FormEvent<HTMLFormElement>, model: PublicModel) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const targets = form.getAll("target").map(String).map((targetID) => ({
        upstream_model_id: targetID,
        priority: Number(form.get(`priority:${targetID}`)),
        weight: Number(form.get(`weight:${targetID}`)),
        enabled: true,
      }));
    setError("");
    setBusy(true);
    try {
      await pocketAIGatewayAdmin.models.updateRoute(model, {
        strategy: String(form.get("strategy")) as RouteStrategy,
        free_only: form.get("free_only") === "on",
        targets,
      });
      await reload();
    } catch (failure) {
      setError(failureText(failure, "Route could not be saved"));
    } finally {
      setBusy(false);
    }
  }

  async function previewModelRoute(event: FormEvent<HTMLFormElement>, model: PublicModel) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const input = {
      operation: String(form.get("operation")),
      streaming: form.get("streaming") === "on",
      estimated_input_tokens: Number(form.get("estimated_input_tokens")),
      estimated_output_tokens: Number(form.get("estimated_output_tokens")),
    };
    setError("");
    setBusy(true);
    try {
      const result = await pocketAIGatewayAdmin.models.previewRoute(model.id, input);
      setPreviews((current) => ({ ...current, [model.id]: { route: result.route, input } }));
    } catch (failure) {
      setError(failureText(failure, "Route preview failed"));
    } finally {
      setBusy(false);
    }
  }

  async function updateCatalog(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    setError("");
    setBusy(true);
    try {
      const result = await pocketAIGatewayAdmin.models.configureCatalog({
        source_url: String(form.get("source_url")),
        refresh_enabled: form.get("refresh_enabled") === "on",
        refresh_interval_hours: Number(form.get("refresh_interval_hours")),
      });
      setCatalogState(result.state);
    } catch (failure) {
      setError(failureText(failure, "Catalog settings could not be saved"));
    } finally {
      setBusy(false);
    }
  }

  async function refreshCatalog() {
    setError("");
    setBusy(true);
    try {
      await pocketAIGatewayAdmin.models.refreshCatalog();
      await reload();
    } catch (failure) {
      setError(failureText(failure, "Catalog refresh failed"));
    } finally {
      setBusy(false);
    }
  }

  return { previews, error, busy, publishModel, updateModelRoute, previewModelRoute, updateCatalog, refreshCatalog };
}
