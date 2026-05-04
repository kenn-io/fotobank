<!-- frontend/src/lib/components/ThreeColumnLayout.svelte -->
<script lang="ts">
  import type { Snippet } from "svelte";

  let { sidebar, main, detail }: {
    sidebar: Snippet;
    main: Snippet;
    detail?: Snippet;
  } = $props();
</script>

<div class="shell">
  <aside class="sidebar">{@render sidebar()}</aside>
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
       Select group button stops colliding with the scrubber. */
    --rail-width: 48px;
  }
  .sidebar {
    background: var(--surface);
    border-right: 1px solid var(--border);
    overflow-y: auto;
    /* Relief — top-edge highlight matches AppHeader so the chrome reads
       as a unified raised plane stepping down from the header. */
    box-shadow: inset 0 1px 0 var(--rim-highlight);
  }
  .main {
    overflow: auto;
    background: var(--bg);
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
    background: var(--surface);
    border-left: 1px solid var(--border);
    overflow-y: auto;
    box-shadow: inset 0 1px 0 var(--rim-highlight);
  }
</style>
