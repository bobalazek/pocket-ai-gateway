import { describe, expect, it } from "vitest";

import { availableCapabilities } from "@/features/providers/utils/provider-capabilities.utils";
import type { ProviderConnection, ProviderPreset } from "@/features/providers/types/providers.types";

const connection = (preset: string, adapter: ProviderConnection["adapter"] = "openai"): ProviderConnection => ({
  id: "connection",
  name: "Provider",
  adapter,
  base_url: "https://example.test/v1",
  enabled: true,
  allow_private_network: false,
  timeout_ms: 60_000,
  preset,
  credential_state: "stored",
  revision: 1,
  created_at: "2026-09-15T00:00:00Z",
  updated_at: "2026-09-15T00:00:00Z",
});

const presets: ProviderPreset[] = [{
  id: "openai",
  label: "OpenAI",
  adapter: "openai",
  base_url: "https://api.openai.com/v1",
  base_url_required: false,
  credential_required: true,
  private_network: false,
  operations: ["responses"],
  documentation_url: "https://developers.openai.com/api/reference/overview",
  reviewed_at: "2026-09-15",
}];

describe("provider capability choices", () => {
  it("offers hosted web search only for the built-in OpenAI preset", () => {
    expect(availableCapabilities(connection("openai"), presets)).toContain("web_search");
    expect(availableCapabilities(connection("custom"), presets)).not.toContain("web_search");
    expect(availableCapabilities(connection("custom", "openai_compatible"), presets)).not.toContain("web_search");
  });
});
