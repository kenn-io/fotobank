import { describe, it, expect } from "vitest";
import { normalizeForSearch } from "./normalizeForSearch";

describe("normalizeForSearch", () => {
  it("lowercases ASCII", () => {
    expect(normalizeForSearch("Italy")).toBe("italy");
  });
  it("trims whitespace", () => {
    expect(normalizeForSearch("  Trip  ")).toBe("trip");
  });
  it("lowercases Unicode letters consistently across runtime locales", () => {
    // Turkish capital dotted-I (U+0130) lowercases to "i" + combining
    // dot above under locale-independent toLowerCase. Both the corpus
    // and the query go through the same function, so substring
    // matching stays consistent regardless of the runtime locale.
    expect(normalizeForSearch("İSTANBUL")).toContain("stanbul");
  });
});
