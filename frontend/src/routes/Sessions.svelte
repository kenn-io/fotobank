<!-- frontend/src/routes/Sessions.svelte -->
<script lang="ts">
  import MonthChunk from "../lib/grid/MonthChunk.svelte";
  import { MediaStore } from "../lib/media/mediaStore.svelte";
  import { groupIntoSessions } from "../lib/sessions/sessionGrouping";
  import { DensityStore } from "../lib/density/densityStore.svelte";
  import DensityControl from "../lib/components/DensityControl.svelte";
  import { api } from "../lib/api/client";

  const store = new MediaStore(api);
  // F1: only the first page is loaded. Library gets infinite scroll
  // through VirtualGrid's IntersectionObserver sentinel; Sessions
  // bypasses VirtualGrid (clusters don't bucket like months) and so
  // caps at MediaStore.loadInitial's first page. A later sub-plan
  // will add a sentinel here once the grouping output is known to
  // exceed one page in real libraries.
  store.loadInitial();
  const density = new DensityStore(api, "sessions");
  density.load();
  const flat = $derived(store.months.flatMap((m) => m.items));
  const sessions = $derived(groupIntoSessions(flat, { gapHours: 4 }));

  // F1: hardcoded width. VirtualGrid uses a ResizeObserver against
  // its grid container, but Sessions bypasses VirtualGrid. A later
  // sub-plan that extracts a shared cell can also share the observed
  // width; until then this gives stable layout for the smoke fixture.
  const CONTAINER_WIDTH = 1100;
</script>

<header style="display:flex; justify-content: flex-end; padding: 6px 12px;">
  <DensityControl store={density} />
</header>

<div style="padding: 8px;">
  {#each sessions as s (s.id)}
    {@const first = s.items[0]}
    {#if first}
      <MonthChunk
        items={s.items.map((m) => ({ id: m.id, aspect: m.aspect, thumbUrl: m.thumbUrl }))}
        label={`${first.taken.toUTCString().slice(0, 16)} · ${s.items.length} photos`}
        options={{ containerWidth: CONTAINER_WIDTH, targetRowHeight: density.targetRowHeight, gap: 4 }}
      >
        {#snippet renderCell(m)}
          <a href={`/media/${m.id}`}>
            <img src={m.thumbUrl} alt="" loading="lazy" decoding="async" style="width:100%;height:100%;object-fit:cover" />
          </a>
        {/snippet}
      </MonthChunk>
    {/if}
  {/each}
</div>

{#if store.loading}
  <div style="padding:12px; color: var(--text-muted)">Loading…</div>
{/if}
{#if store.months.length === 0 && !store.loading}
  <div style="padding:24px; color: var(--text-secondary)">No photos yet.</div>
{/if}
