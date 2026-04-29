<script lang="ts">
  let {
    onCreate,
    onCancel,
  }: {
    onCreate: (name: string) => Promise<void>;
    onCancel?: () => void;
  } = $props();

  let name = $state("");
  let error = $state<string | null>(null);
  let pending = $state(false);

  const trimmed = $derived(name.trim());
  const valid = $derived(trimmed.length >= 1 && trimmed.length <= 200);

  async function submit(e: Event) {
    e.preventDefault();
    if (!valid || pending) return;
    pending = true;
    error = null;
    try {
      await onCreate(trimmed);
      name = "";
    } catch (err: unknown) {
      error = (err as { message?: string })?.message ?? "Failed to create album";
    } finally {
      pending = false;
    }
  }
</script>

<form onsubmit={submit} class="new-album-form">
  <label>
    <span>Name</span>
    <!-- svelte-ignore a11y_autofocus -->
    <input
      type="text"
      bind:value={name}
      maxlength="200"
      autofocus
      placeholder="Album name"
      aria-invalid={error !== null}
    />
  </label>
  {#if error}<div class="error" role="alert">{error}</div>{/if}
  <div class="actions">
    {#if onCancel}<button type="button" onclick={onCancel} disabled={pending}>Cancel</button>{/if}
    <button type="submit" disabled={!valid || pending}>{pending ? "Creating…" : "Create"}</button>
  </div>
</form>

<style>
  .new-album-form { display: flex; flex-direction: column; gap: 12px; }
  label { display: flex; flex-direction: column; gap: 4px; }
  .error { color: var(--danger); font-size: 13px; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; }
</style>
