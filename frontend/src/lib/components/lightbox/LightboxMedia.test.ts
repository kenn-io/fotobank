import { describe, it, expect, vi } from "vitest";
import { render } from "@testing-library/svelte";
import LightboxMedia from "./LightboxMedia.svelte";

// panzoom is invoked on image mount; mock it so jsdom doesn't choke.
vi.mock("panzoom", () => ({
  default: () => ({
    dispose: vi.fn(),
    getTransform: () => ({ scale: 1, x: 0, y: 0 }),
    zoomTo: vi.fn(),
    zoomAbs: vi.fn(),
  }),
}));

describe("LightboxMedia", () => {
  it("renders <img> for image kind", () => {
    const { container } = render(LightboxMedia, {
      props: { kind: "image", src: "/x", alt: "" },
    });
    expect(container.querySelector("img")).toBeTruthy();
    expect(container.querySelector("video")).toBeNull();
  });

  it("renders <video> for video kind", () => {
    const { container } = render(LightboxMedia, {
      props: { kind: "video", src: "/v", alt: "" },
    });
    expect(container.querySelector("video")).toBeTruthy();
    expect(container.querySelector("img")).toBeNull();
  });
});
