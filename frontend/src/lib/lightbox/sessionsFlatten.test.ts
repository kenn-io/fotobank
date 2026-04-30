import { describe, it, expect } from "vitest";
import { flattenSessionIds, flattenLibraryIds } from "./sessionsFlatten";
import type { Month } from "../media/mediaStore.svelte";

const months: Month[] = [
  {
    key: "2026-04",
    items: [
      { id: "a", timestamp: "", aspect: 1, thumbUrl: "", taken: new Date("2026-04-20"), thumbVersion: 0 },
      { id: "b", timestamp: "", aspect: 1, thumbUrl: "", taken: new Date("2026-04-10"), thumbVersion: 0 },
    ],
  },
  {
    key: "2026-03",
    items: [
      { id: "c", timestamp: "", aspect: 1, thumbUrl: "", taken: new Date("2026-03-15"), thumbVersion: 0 },
    ],
  },
];

describe("flattenLibraryIds / flattenSessionIds", () => {
  it("flattens months in declared order", () => {
    expect(flattenLibraryIds(months)).toEqual(["a", "b", "c"]);
    expect(flattenSessionIds(months)).toEqual(["a", "b", "c"]);
  });

  it("handles empty months", () => {
    expect(flattenLibraryIds([])).toEqual([]);
    expect(flattenSessionIds([])).toEqual([]);
  });
});
