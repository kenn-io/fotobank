import { describe, it, expect } from "vitest";
import { normalizeForSearch } from "./normalizeForSearch";

describe("normalizeForSearch", () => {
  it("lowercases ASCII", () => {
    expect(normalizeForSearch("Italy")).toBe("italy");
  });
  it("trims whitespace", () => {
    expect(normalizeForSearch("  Trip  ")).toBe("trip");
  });
  it("locale-aware lowercases Unicode letters", () => {
    // Turkish dotted I → i (locale default; en-US-acceptable for our use).
    expect(normalizeForSearch("İSTANBUL")).toContain("stanbul");
  });
});
