import { describe, it, expect } from "vitest";
import { flattenSessionIds, flattenLibraryIds } from "./sessionsFlatten";
import type { Month } from "../media/mediaStore.svelte";

const months: Month[] = [
  {
    key: "2026-04",
    items: [
      { id: "a", timestamp: "", aspect: 1, thumbUrl: "", thumbStatus: "ready", taken: new Date("2026-04-20"), thumbVersion: 0 },
      { id: "b", timestamp: "", aspect: 1, thumbUrl: "", thumbStatus: "ready", taken: new Date("2026-04-10"), thumbVersion: 0 },
    ],
  },
  {
    key: "2026-03",
    items: [
      { id: "c", timestamp: "", aspect: 1, thumbUrl: "", thumbStatus: "ready", taken: new Date("2026-03-15"), thumbVersion: 0 },
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

  it("session-clusters items in chronological order within each cluster", () => {
    // Three items inside the same 4-hour cluster, plus one days later.
    // Library order is descending (the user sees newest first), but
    // within a session the visible Sessions grid renders ascending —
    // flattenSessionIds must match that grid order so the lightbox
    // prev/next walks the same sequence the user clicks.
    const dense: Month[] = [
      {
        key: "2026-04",
        items: [
          { id: "later",  timestamp: "", aspect: 1, thumbUrl: "", thumbStatus: "ready", taken: new Date("2026-04-22T12:00:00Z"), thumbVersion: 0 },
          { id: "third",  timestamp: "", aspect: 1, thumbUrl: "", thumbStatus: "ready", taken: new Date("2026-04-20T13:00:00Z"), thumbVersion: 0 },
          { id: "second", timestamp: "", aspect: 1, thumbUrl: "", thumbStatus: "ready", taken: new Date("2026-04-20T12:30:00Z"), thumbVersion: 0 },
          { id: "first",  timestamp: "", aspect: 1, thumbUrl: "", thumbStatus: "ready", taken: new Date("2026-04-20T12:00:00Z"), thumbVersion: 0 },
        ],
      },
    ];
    // Library ignores grouping → walks declared (descending) order.
    expect(flattenLibraryIds(dense)).toEqual(["later", "third", "second", "first"]);
    // Sessions reorders: newest session first ([later]), then the
    // earlier cluster ([first, second, third]) ascending within.
    expect(flattenSessionIds(dense)).toEqual(["later", "first", "second", "third"]);
  });
});
