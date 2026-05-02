<!-- frontend/src/routes/MediaDetail.svelte -->
<!-- Dispatcher: with ?from=, mount Lightbox; without, mount DirectMediaDetail. -->
<script lang="ts">
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
  import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";
  import type { ToastStore } from "../lib/toasts/toastStore.svelte";
  import type { AppConfigStore } from "../lib/app/appConfig.svelte";
  import DirectMediaDetail from "../lib/components/DirectMediaDetail.svelte";
  import Lightbox from "../lib/components/lightbox/Lightbox.svelte";

  let { id, from, mediaStore, albumsStore, hiddenStore, toastStore, appConfig }: {
    id: string;
    from: string | undefined;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
    appConfig: AppConfigStore;
  } = $props();
</script>

{#if from === undefined}
  <DirectMediaDetail {id} {mediaStore} {albumsStore} {hiddenStore} {toastStore} {appConfig} />
{:else}
  <Lightbox {id} {from} {mediaStore} {albumsStore} {hiddenStore} {toastStore} {appConfig} />
{/if}
