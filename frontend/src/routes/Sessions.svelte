<!-- frontend/src/routes/Sessions.svelte -->
<script lang="ts">
  import MonthChunk from "../lib/grid/MonthChunk.svelte";
  import { MediaStore } from "../lib/media/mediaStore.svelte";
  import { groupIntoSessions } from "../lib/sessions/sessionGrouping";
  import { DensityStore } from "../lib/density/densityStore.svelte";
  import DensityControl from "../lib/components/DensityControl.svelte";
  import { api } from "../lib/api/client";

  const store = new MediaStore(api);
  store.loadInitial();
  const density = new DensityStore(api, "sessions");
  density.load();
  const flat = $derived(store.months.flatMap((m) => m.items));
  const sessions = $derived(groupIntoSessions(flat, { gapHours: 4 }));

  let containerEl: HTMLDivElement | null = $state(null);
  let containerWidth = $state(800);
  let sentinel: HTMLDivElement | null = $state(null);

  $effect(() => {
    if (!containerEl) return;
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width;
      if (w) containerWidth = w;
    });
    ro.observe(containerEl);
    return () => ro.disconnect();
  });

  // Pull more pages when the user scrolls near the bottom — sessions
  // cluster across the full library, so capping at the first page hides
  // older trips. The 800px rootMargin matches VirtualGrid's so the next
  // page is in flight before the sentinel is on-screen.
  $effect(() => {
    if (!sentinel) return;
    const io = new IntersectionObserver((entries) => {
      if (entries[0]?.isIntersecting) store.loadMore();
    }, { rootMargin: "800px 0px" });
    io.observe(sentinel);
    return () => io.disconnect();
  });
</script>

<header style="display:flex; justify-content: flex-end; padding: 6px 12px;">
  <DensityControl store={density} />
</header>

<div bind:this={containerEl} style="padding: 8px;">
  {#each sessions as s (s.id)}
    {@const first = s.items[0]}
    {#if first}
      <MonthChunk
        items={s.items.map((m) => ({ id: m.id, aspect: m.aspect, thumbUrl: m.thumbUrl }))}
        label={`${first.taken.toUTCString().slice(0, 16)} · ${s.items.length} photos`}
        options={{ containerWidth, targetRowHeight: density.targetRowHeight, gap: 4 }}
      >
        {#snippet renderCell(m)}
          <a href={`/media/${m.id}`}>
            <img src={m.thumbUrl} alt="" loading="lazy" decoding="async" style="width:100%;height:100%;object-fit:cover" />
          </a>
        {/snippet}
      </MonthChunk>
    {/if}
  {/each}
  <div bind:this={sentinel} style="height:1px"></div>
</div>

{#if store.loading}
  <div style="padding:12px; color: var(--text-muted)">Loading…</div>
{/if}
{#if store.months.length === 0 && !store.loading}
  <div style="padding:24px; color: var(--text-secondary)">No photos yet.</div>
{/if}
