import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import { RequestList } from "@/features/requests/components/request-list";
import type { GatewayRequest } from "@/features/requests/types/requests.types";

const request = {
  id: "req_1",
  model_id: "assistant",
  dialect: "openai",
  operation: "chat/completions",
  state: "succeeded",
  started_at: "2026-09-24T12:00:00Z",
  attempts: [
    { id: "att_1", connection_id: "old_connection", input_tokens: 20, output_tokens: 10, cost_usd: "0.02" },
    { id: "att_2", connection_id: "new_connection", input_tokens: 3, output_tokens: 5, cost_usd: "0.000031" },
  ],
} as GatewayRequest;

describe("RequestList", () => {
  it("summarizes the latest attempt and keeps filters in the detail link", () => {
    const html = renderToStaticMarkup(<RequestList items={[request]} filters={{ model_id: "assistant", cursor: "next" }} next="" onNavigate={vi.fn()} onOpenDetail={vi.fn()} />);

    expect(html).toContain("1 request on this page");
    expect(html).toContain("2 attempts");
    expect(html).toContain('<td data-label="Tokens">8</td>');
    expect(html).toContain('<td data-label="Cost">$0.000031</td>');
    expect(html).toContain("new_connection");
    expect(html).not.toContain("old_connection");
    expect(html).toContain("?request_id=req_1&amp;model_id=assistant");
    expect(html).not.toContain("cursor=next");
    expect(html).not.toContain("Admission estimate");
  });

  it("distinguishes unknown usage from known zero", () => {
    const missing = renderToStaticMarkup(<RequestList items={[{ ...request, attempts: [] }]} filters={{}} next="" onNavigate={vi.fn()} onOpenDetail={vi.fn()} />);
    const zero = renderToStaticMarkup(<RequestList items={[{ ...request, attempts: [{ ...request.attempts[0], input_tokens: 0, output_tokens: 0, cost_usd: "0" }] }]} filters={{}} next="" onNavigate={vi.fn()} onOpenDetail={vi.fn()} />);

    expect(missing).toContain('<td data-label="Tokens">Unknown</td>');
    expect(missing).toContain('<td data-label="Cost">Unknown</td>');
    expect(zero).toContain('<td data-label="Tokens">0</td>');
    expect(zero).toContain('<td data-label="Cost">$0</td>');
  });
});
