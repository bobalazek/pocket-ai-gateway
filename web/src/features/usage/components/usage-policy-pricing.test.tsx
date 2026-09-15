import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { PriceSection } from "@/features/usage/components/usage-policy-pricing";
import type { PricePreview, PriceVersion } from "@/features/usage/types/usage.types";

const price: PriceVersion = {
  id: "price_1",
  connection_id: "conn_1",
  model_id: "assistant",
  input_usd_per_million: "1.00",
  cache_read_usd_per_million: "0.10",
  output_usd_per_million: "3.00",
  source: "provider pricing",
  effective_from: "2026-09-15T00:00:00Z",
  effective_to: null,
  weekly_start_minute_utc: 540,
  weekly_end_minute_utc: 6_780,
  created_at: "2026-09-15T00:00:00Z",
};

describe("PriceSection", () => {
  it("labels the cache rate and UTC schedule and renders readable saved and preview terms", () => {
    const preview: PricePreview = {
      connection_id: price.connection_id,
      model_id: price.model_id,
      input_usd_per_million: price.input_usd_per_million,
      cache_read_usd_per_million: price.cache_read_usd_per_million ?? null,
      output_usd_per_million: price.output_usd_per_million,
      source: price.source,
      effective_from: price.effective_from,
      effective_to: "",
      weekly_start_minute_utc: price.weekly_start_minute_utc ?? null,
      weekly_end_minute_utc: price.weekly_end_minute_utc ?? null,
    };
    const html = renderToStaticMarkup(
      <PriceSection prices={[price]} outbox={null} cursor="" preview={preview} busy={false} onLoadMore={() => undefined} onSubmit={() => undefined} onCancel={() => undefined} />,
    );

    expect(html).toContain('for="cache_read_price"');
    expect(html).toContain("Cache-read USD / million");
    expect(html).toContain('aria-describedby="cache-read-price-help"');
    expect(html).toContain("Weekly price window (UTC)");
    expect(html).toContain('aria-describedby="weekly-window-help"');
    expect(html).toContain("$0.10 cache read");
    expect(html).toContain("Monday 09:00–Friday 17:00 UTC");
  });

  it("labels legacy prices without a cache-read rate as unpriced", () => {
    const html = renderToStaticMarkup(
      <PriceSection prices={[{ ...price, cache_read_usd_per_million: null, weekly_start_minute_utc: null, weekly_end_minute_utc: null }]} outbox={null} cursor="" preview={null} busy={false} onLoadMore={() => undefined} onSubmit={() => undefined} onCancel={() => undefined} />,
    );

    expect(html).toContain("cache reads unpriced");
    expect(html).toContain("All week");
  });
});
