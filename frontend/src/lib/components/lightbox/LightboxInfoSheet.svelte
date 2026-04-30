<!-- frontend/src/lib/components/lightbox/LightboxInfoSheet.svelte -->
<script lang="ts">
  import type { Media } from "../../media/mediaStore.svelte";
  import BottomSheet from "../BottomSheet.svelte";
  import LightboxMetadata from "./LightboxMetadata.svelte";

  let { media, onClose }: { media: Media; onClose: () => void } = $props();
  const sheetId = $derived(`lb-info-${media.id}`);
</script>

<BottomSheet id={sheetId} {onClose} snap="peek">
  <LightboxMetadata {media} />
</BottomSheet>
