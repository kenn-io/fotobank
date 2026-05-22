import { describe, it, expect } from "vitest";
import { render } from "@testing-library/svelte";
import AppHeader from "./AppHeader.svelte";
import type { Principal } from "../app/appConfig.svelte";

const principal: Principal = {
  hub: "dev-local",
  userId: "owner",
  handle: "owner",
};

function renderHeader(overrides: Record<string, unknown> = {}) {
  return render(AppHeader, {
    props: {
      principal,
      ready: true,
      onsearch: () => {},
      ...overrides,
    },
  });
}

describe("AppHeader", () => {
  it("renders the fotobank brand mark", () => {
    const { container } = renderHeader();
    const brand = container.querySelector(".brand");
    expect(brand).not.toBeNull();
    expect(brand?.textContent).toContain("fotobank");
  });

  it("does not render an in-header section nav (sidebar owns navigation)", () => {
    const { container } = renderHeader();
    expect(container.querySelector("nav.tabs")).toBeNull();
    expect(container.querySelector("header.top nav")).toBeNull();
  });

  it("exposes the search input via data-testid", () => {
    const { getByTestId } = renderHeader();
    const input = getByTestId("search-input") as HTMLInputElement;
    expect(input).toBeTruthy();
    expect(input.tagName).toBe("INPUT");
    expect(input.type).toBe("search");
  });

  it("mounts identity chips when ready=true", () => {
    const { getByTestId } = renderHeader({ ready: true });
    expect(getByTestId("id-chip-hub")).toBeTruthy();
    expect(getByTestId("id-chip-user")).toBeTruthy();
  });

  it("does NOT render identity chips while ready=false", () => {
    const { queryByTestId } = renderHeader({ ready: false });
    expect(queryByTestId("id-chip-hub")).toBeNull();
    expect(queryByTestId("id-chip-user")).toBeNull();
  });
});
