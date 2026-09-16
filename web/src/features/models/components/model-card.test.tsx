import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import { ModelCard } from "@/features/models/components/model-card";
import type { useModels } from "@/features/models/hooks/use-models";
import type { PublicModel } from "@/features/models/types/models.types";

const model: PublicModel = {
  id: "assistant",
  label: "Assistant",
  description: "",
  target_connection_id: "connection",
  target_model_id: "target_allowed",
  upstream_id: "eligible-model",
  adapter: "openai",
  adapter_label: "OpenAI",
  capabilities: ["chat_completions"],
  capability_details: [{ id: "chat_completions", label: "Chat completions" }],
  active: true,
  revision: 1,
  routing_strategy: "weighted",
  free_only: false,
  routing_policy: {
    allowed_strategies: ["weighted"],
    max_targets_by_strategy: { weighted: 2 },
    free_only_allowed: true,
    free_only_label: "Use models with verified free pricing",
    priority_field: { label: "Route priority", min: 2, max: 20, default: 3 },
    weight_field: { label: "Traffic weight", min: 4, max: 40, default: 5 },
    strategies: [{ id: "weighted", label: "Weighted" }],
  },
};

describe("ModelCard", () => {
  it("renders backend-owned route metadata", () => {
    const available = { id: "target_allowed", connection_id: "connection", upstream_id: "eligible-model", capabilities: ["chat_completions"], capability_details: model.capability_details, active: true };
    const secondAvailable = { ...available, id: "target_second", upstream_id: "second-model" };
    const unavailable = { ...available, id: "target_blocked", upstream_id: "blocked-model" };
    const dashboard = {
      manager: true,
      targets: [available, unavailable],
      availableTargets: { assistant: [available, secondAvailable] },
      selectedStrategies: {},
      selectStrategy: vi.fn(),
      routes: {},
      previews: {
        assistant: {
          route: { model_id: "assistant", strategy: "weighted", free_only: false, selection_reason: "weighted", selection_message: "Eligible model selected by traffic weight.", targets: [{ upstream_model_id: "target_allowed", connection_id: "connection", upstream_id: "eligible-model", adapter: "openai", capabilities: ["chat"], priority: 1, weight: 1, sample_count: 0 }], rejected: [{ upstream_model_id: "target_blocked", connection_id: "connection", reason: "missing_capability", message: "Blocked model does not support this operation." }] },
          input: { operation: "chat/completions", streaming: false, estimated_input_tokens: 100, estimated_output_tokens: 50 },
        },
      },
      busy: false,
      preview: vi.fn(),
      saveRoute: vi.fn(),
    } as unknown as ReturnType<typeof useModels>;

    const html = renderToStaticMarkup(<ModelCard item={model} dashboard={dashboard} />);

    expect(html).toContain("Chat completions");
    expect(html).toContain("eligible-model");
    expect(html).not.toContain("blocked-model");
    expect(html).toContain("Use models with verified free pricing");
    expect(html).toContain('aria-label="eligible-model Route priority"');
    expect(html).toContain('min="2" max="20" name="priority:target_allowed" value="3"');
    expect(html).toContain('name="priority:target_second" value="4"');
    expect(html).toContain('aria-label="eligible-model Traffic weight"');
    expect(html).toContain('min="4" max="40" name="weight:target_allowed" value="5"');
    expect(html).toContain('type="checkbox" name="target" value="target_allowed"');
    expect(html).toContain("Eligible model selected by traffic weight.");
    expect(html).toContain("Blocked model does not support this operation.");
    expect(html).not.toContain("missing_capability");
  });
});
