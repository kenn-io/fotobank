import { render } from "@testing-library/svelte";
import { describe, it, expect } from "vitest";
import Sidebar from "./Sidebar.svelte";

describe("Sidebar grouped entries", () => {
  it("renders BROWSE / CURATE / MANAGE group headers in order", () => {
    const { container } = render(Sidebar, { props: { active: "library" } });
    const headers = Array.from(container.querySelectorAll(".group-header"))
      .map((el) => el.textContent?.trim());
    expect(headers).toEqual(["BROWSE", "CURATE", "MANAGE"]);
  });

  it("renders Library + Sessions under BROWSE", () => {
    const { container } = render(Sidebar, { props: { active: "library" } });
    const browseGroup = container.querySelector(".group[data-group='browse']");
    expect(browseGroup?.textContent).toContain("Library");
    expect(browseGroup?.textContent).toContain("Sessions");
  });

  it("renders Albums under CURATE", () => {
    const { container } = render(Sidebar, { props: { active: "albums" } });
    const curate = container.querySelector(".group[data-group='curate']");
    expect(curate?.textContent).toContain("Albums");
  });

  it("renders Shares under MANAGE", () => {
    const { container } = render(Sidebar, { props: { active: "shares" } });
    const manage = container.querySelector(".group[data-group='manage']");
    expect(manage?.textContent).toContain("Shares");
  });

  it("highlights the active entry", () => {
    const { container } = render(Sidebar, { props: { active: "albums" } });
    const active = container.querySelector("a.active");
    expect(active?.textContent?.trim()).toBe("Albums");
  });
});
