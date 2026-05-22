import { render, fireEvent } from "@testing-library/svelte";
import { describe, it, expect, beforeEach } from "vitest";
import GroupSelectButton from "./GroupSelectButton.svelte";
import { selection } from "../selection/selectionStore.svelte";

describe("GroupSelectButton", () => {
  beforeEach(() => { selection.clear(); });

  it("renders 'Select group' when no ids are selected", () => {
    const { getByRole } = render(GroupSelectButton, {
      props: { ids: ["a", "b"], label: "April 2024" },
    });
    const btn = getByRole("button");
    expect(btn.textContent?.trim()).toBe("Select group");
    expect(btn.getAttribute("aria-label")).toBe("Select 2 photos in April 2024");
  });

  it("renders 'Deselect group' when all ids are already selected", () => {
    selection.addAll(["a", "b"]);
    const { getByRole } = render(GroupSelectButton, {
      props: { ids: ["a", "b"], label: "April 2024" },
    });
    const btn = getByRole("button");
    expect(btn.textContent?.trim()).toBe("Deselect group");
    expect(btn.getAttribute("aria-label")).toBe("Deselect 2 photos in April 2024");
  });

  it("clicking adds all ids when none are selected", async () => {
    const { getByRole } = render(GroupSelectButton, {
      props: { ids: ["a", "b", "c"], label: "April" },
    });
    await fireEvent.click(getByRole("button"));
    expect(selection.ids.has("a")).toBe(true);
    expect(selection.ids.has("b")).toBe(true);
    expect(selection.ids.has("c")).toBe(true);
    expect(selection.lastAnchor).toBe("c");
  });

  it("clicking removes all ids when every id is already selected", async () => {
    selection.addAll(["a", "b"]);
    selection.toggle("anchor"); // sets lastAnchor to "anchor"
    selection.addAll(["a", "b"]); // re-adds; lastAnchor moves to "b"
    const { getByRole } = render(GroupSelectButton, {
      props: { ids: ["a", "b"], label: "April" },
    });
    await fireEvent.click(getByRole("button"));
    expect(selection.ids.has("a")).toBe(false);
    expect(selection.ids.has("b")).toBe(false);
    // lastAnchor must NOT change on removeAll.
    expect(selection.lastAnchor).toBe("b");
  });
});
