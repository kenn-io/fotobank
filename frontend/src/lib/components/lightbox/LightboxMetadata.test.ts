import { describe, it, expect } from "vitest";
import { render } from "@testing-library/svelte";
import LightboxMetadata from "./LightboxMetadata.svelte";

const baseMedia = {
  id: "m1",
  timestamp: "2026-04-20T12:00:00Z",
  taken: new Date("2026-04-20T12:00:00Z"),
  aspect: 1,
  thumbUrl: "/g",
  thumbVersion: 0,
  original_filename: "IMG_001.JPG",
  size: 1024 * 1024 * 5,
  location_label: "Paris, France",
};

describe("LightboxMetadata", () => {
  it("renders existing fields only (no caption/rating/AI tags)", () => {
    const { container, getAllByText, getByText } = render(LightboxMetadata, {
      props: { media: baseMedia } as never,
    });
    // Filename appears twice (File row and Download link), so use getAllByText.
    expect(getAllByText("IMG_001.JPG").length).toBeGreaterThan(0);
    expect(getByText(/5\.0 MB/)).toBeTruthy();
    expect(getByText("Paris, France")).toBeTruthy();
    expect(container.textContent ?? "").not.toMatch(/Rating|Caption|AI/);
  });
});
