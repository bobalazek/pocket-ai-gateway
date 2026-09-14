import { describe, expect, it } from "vitest";

import { unauthenticatedDestination } from "./dashboard-routing";

describe("dashboard routing", () => {
  it("only leaves completed setup open for recovery", () => {
    expect(unauthenticatedDestination("/_/setup/", false)).toBe("/_/login/");
    expect(unauthenticatedDestination("/_/setup/", true)).toBeNull();
  });
});
