<script lang="ts">
  import ActionBar from "./ActionBar.svelte";
  import { selection } from "../selection/selectionStore.svelte";
</script>

<ActionBar {selection}>
  {#snippet actions()}
    <button type="button" class="test-fix">Test Action</button>
  {/snippet}
</ActionBar>
