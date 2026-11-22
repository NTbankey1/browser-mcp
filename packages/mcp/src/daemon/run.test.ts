import { describe, expect, it } from "vitest";

import { idleExitMsFromEnv } from "@/daemon/run";

describe("idleExitMsFromEnv", () => {
  it("parses a positive value, 0 and invalid input", () => {
    expect(idleExitMsFromEnv("3600000")).toBe(3_600_000);
    expect(idleExitMsFromEnv("0")).toBe(0);
    expect(idleExitMsFromEnv("")).toBeUndefined();
    expect(idleExitMsFromEnv("not-a-number")).toBeUndefined();
    expect(idleExitMsFromEnv(undefined)).toBeUndefined();
  });
});
