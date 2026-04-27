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
        options={{ containerWidth: 1100, targetRowHeight: density.targetRowHeight, gap: 4 }}
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
