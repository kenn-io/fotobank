<!-- frontend/src/lib/components/ThreeColumnLayout.svelte -->
<script lang="ts">
  import { untrack, type Snippet } from "svelte";

  let { sidebar, main, detail, routeKey = "" }: {
    sidebar: Snippet;
    main: Snippet;
    detail?: Snippet;
    routeKey?: string;
  } = $props();
  let navigationOpen = $state(false);
  let toggle: HTMLButtonElement;
  let previousSection: string | undefined;

  function closeNavigation() {
    navigationOpen = false;
    toggle?.focus();
  }

  // Keep filters open while refining a query, but return to content when
  // choosing a different section. Both views use the same sidebar instance.
  $effect(() => {
    const section = routeKey;
    untrack(() => {
      if (section !== previousSection && navigationOpen) closeNavigation();
      previousSection = section;
    });
  });
</script>

<svelte:window onkeydown={(event) => {
  if (event.key === "Escape" && navigationOpen) closeNavigation();
}} />

<div class="shell" class:navigation-open={navigationOpen}>
  <div class="mobile-navigation">
    <button bind:this={toggle} type="button" aria-expanded={navigationOpen}
      aria-controls="browse-sidebar" onclick={() => (navigationOpen = !navigationOpen)}>
      {navigationOpen ? "Close browse & filters" : "Browse & filters"}
    </button>
  </div>
  <aside id="browse-sidebar" class="sidebar">{@render sidebar()}</aside>
  <section class="main">{@render main()}</section>
  {#if detail}
    <aside class="detail">{@render detail()}</aside>
  {/if}
</div>

<style>
  .shell {
    display: grid;
    grid-template-columns: 220px 1fr auto;
    /* Subtract the AppHeader height (46px, fixed in AppHeader.svelte's
       `header.top` rule). With plain `100vh` the shell sits below the
       header, total document height becomes 100vh + 46px, and the body
       scrolls 46px — which surfaces as the "phantom right-edge
       scrollbar" the user reported. */
    height: calc(100vh - 46px);
    width: 100vw;
    /* Reserved width for the timeline rail (right side of .main).
       YearScrubber is position:fixed and reads `right` from the
       viewport, so the scrubber and this rail must agree on width.
       Anything that sits inside .main (DensityControl strip, photo
       grid day-header) bottoms out at the rail's left edge so the
       Select group button stops colliding with the scrubber.
       64px = ~17px scrollbar gutter + ~47px of usable rail. The
       earlier 48px value left only ~31px of usable rail after the
       scrollbar, which crowded the year ticks against both the
       scrollbar AND the day-header's Select-group buttons. */
    --rail-width: 64px;
  }
  .mobile-navigation { display: none; }
  .sidebar {
    background: var(--bg-surface);
    border-right: 1px solid var(--border-default);
    overflow-y: auto;
    /* Relief — top-edge highlight matches AppHeader so the chrome reads
       as a unified raised plane stepping down from the header. */
    box-shadow: inset 0 1px 0 var(--fb-rim-highlight);
  }
  .main {
    min-width: 0;
    overflow: auto;
    background: var(--bg-primary);
    /* Right-side rail: padding-right reserves the gutter that
       YearScrubber lives in. The browser places the overflow
       scrollbar at .main's outer right edge (outside the padding),
       so the visual order — scrolling content → rail gutter →
       scrollbar — is exactly what the design needs. The DensityControl
       header and the VirtualGrid both sit inside the padded content
       box, so neither bleeds under the scrubber.
       scrollbar-gutter: stable keeps the layout from twitching when
       overflow toggles (a short library doesn't need a scrollbar,
       a long one does — without `stable` the rail width would
       briefly compress on the transition). */
    padding-right: var(--rail-width);
    scrollbar-gutter: stable;
  }
  .detail {
    background: var(--bg-surface);
    border-left: 1px solid var(--border-default);
    overflow-y: auto;
    box-shadow: inset 0 1px 0 var(--fb-rim-highlight);
  }
  @media (max-width: 760px) {
    .shell {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto minmax(0, 1fr);
      height: calc(100dvh - 102px);
      --rail-width: 0px;
    }
    .mobile-navigation {
      display: flex;
      padding: 4px 12px;
      border-bottom: 1px solid var(--border-default);
      background: var(--bg-surface);
    }
    .mobile-navigation button {
      min-height: 44px;
      padding: 8px 12px;
      color: var(--text-primary);
      background: var(--bg-inset);
      border: 1px solid var(--border-default);
      border-radius: 4px;
      font: inherit;
      cursor: pointer;
    }
    .mobile-navigation button:focus-visible {
      outline: 2px solid var(--accent-blue);
      outline-offset: 2px;
    }
    .sidebar { display: none; border-right: 0; }
    .sidebar :global(.entry), .sidebar :global(button) {
      min-height: 44px;
      font-size: 14px;
    }
    .navigation-open .sidebar { display: block; }
    .navigation-open .main { display: none; }
    .main { scrollbar-gutter: auto; }
  }
</style>
