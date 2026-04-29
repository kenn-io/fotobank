import { render } from "@testing-library/svelte";
import { describe, it, expect } from "vitest";
import { selection } from "../selection/selectionStore.svelte";
import ActionsFixture from "./ActionsFixture.svelte";

describe("ActionBar with actions snippet", () => {
  it("renders the actions snippet beside the count", () => {
    selection.clear();
    selection.toggle("a");
    selection.toggle("b");
    const { container } = render(ActionsFixture);
    expect(container.querySelector(".count")?.textContent).toContain("2 selected");
    expect(container.querySelector("button.test-fix")).not.toBeNull();
  });
});
