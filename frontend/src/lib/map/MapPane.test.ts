import { render } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vitest";
import MapPane from "./MapPane.svelte";

// Heavier behavior — cluster click, marker click, viewport sync — is
// covered by Playwright e2e (Section I) where Leaflet runs against a
// real DOM. JSDOM doesn't implement SVG/canvas paths reliably enough
// to assert on rendered tiles, marker icons, or cluster bubbles, so
// this smoke test only checks that mounting doesn't throw and the
// container is attached.
describe("MapPane", () => {
  it("renders an empty container when items is empty", () => {
    const { getByTestId } = render(MapPane, {
      props: emptyMapPaneProps(),
    });
    const el = getByTestId("map-pane");
    expect(el).toBeTruthy();
  });
});

function emptyMapPaneProps() {
  return {
    items: [],
    initialZoom: undefined,
    initialCenter: undefined,
    focusId: undefined,
    onMarkerClick: vi.fn(),
    onClusterClick: vi.fn(),
    onViewportChange: vi.fn(),
    onClearClusterFilter: vi.fn(),
  };
}
