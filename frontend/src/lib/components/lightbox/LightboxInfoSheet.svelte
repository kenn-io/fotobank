<!-- frontend/src/lib/components/lightbox/LightboxInfoSheet.svelte -->
<script lang="ts">
  import type { Media } from "../../media/mediaStore.svelte";
  import BottomSheet from "../BottomSheet.svelte";
  import LightboxMetadata from "./LightboxMetadata.svelte";

  let { media, onClose }: { media: Media; onClose: () => void } = $props();
  // Per-instance stable id. Earlier we tied this to media.id, but
  // BottomSheet registers its modalStack entry on mount and pops
  // using the captured id on destroy — if the sheet stays mounted
  // while the user navigates between photos, the id would drift and
  // the original stack entry would never be popped. The sheet is
  // owned by the surrounding lightbox, so a one-per-instance random
  // id keeps the registration symmetric.
  const sheetId = `lb-info-${Math.random().toString(36).slice(2)}`;
</script>

<BottomSheet id={sheetId} {onClose} snap="peek">
  <LightboxMetadata {media} />
</BottomSheet>
