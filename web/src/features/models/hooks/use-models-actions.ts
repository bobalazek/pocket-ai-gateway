"use client";

import { useState, type Dispatch, type SetStateAction } from "react";

import type { CatalogSettingsInput, CatalogState, CreatePublicModelInput, PublicModel, RoutePlan, RoutePreviewInput, UpdateRouteInput } from "@/features/models/types/models.types";
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

  async function publishModel(input: CreatePublicModelInput) {
    setError("");
    setBusy(true);
    try {
      await pocketAIGatewayAdmin.models.createPublicModel(input);
      setSelectedTarget("");
      await reload();
      return true;
    } catch (failure) {
      setError(failureText(failure, "Model could not be published"));
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function updateModelRoute(model: PublicModel, input: UpdateRouteInput) {
    setError("");
    setBusy(true);
    try {
      await pocketAIGatewayAdmin.models.updateRoute(model, input);
      await reload();
    } catch (failure) {
      setError(failureText(failure, "Route could not be saved"));
    } finally {
      setBusy(false);
    }
  }

  async function previewModelRoute(model: PublicModel, input: RoutePreviewInput) {
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

  async function updateCatalog(input: CatalogSettingsInput) {
    setError("");
    setBusy(true);
    try {
      const result = await pocketAIGatewayAdmin.models.configureCatalog(input);
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
