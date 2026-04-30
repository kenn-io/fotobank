<!-- frontend/src/lib/components/lightbox/Lightbox.svelte -->
<!-- Temporary stub — replaced in Task 13. Until then, mount the
     direct-detail UI so /media/:id?from=... links don't degrade to
     placeholder text during the build-up tasks. The `from` prop is
     accepted but ignored; T13 wires it through. -->
<script lang="ts">
  import type { MediaStore } from "../../media/mediaStore.svelte";
  import type { AlbumsStore } from "../../albums/albumsStore.svelte";
  import type { HiddenStore } from "../../hidden/hiddenStore.svelte";
  import type { ToastStore } from "../../toasts/toastStore.svelte";
  import DirectMediaDetail from "../DirectMediaDetail.svelte";

  let { id, mediaStore, albumsStore, hiddenStore, toastStore }: {
    id: string;
    from: string;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
  } = $props();
</script>

<DirectMediaDetail {id} {mediaStore} {albumsStore} {hiddenStore} {toastStore} />
