import { describe, expect, it } from "vitest";

import { unauthenticatedDestination } from "./dashboard-routing";

describe("dashboard routing", () => {
  it("sends completed setup to login", () => {
    expect(unauthenticatedDestination("/_/setup/")).toBe("/_/login/");
  });
});
