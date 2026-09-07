<script lang="ts">
  import { Button, TextInput } from "@kenn-io/kit-ui";

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
      // huma errors come back as `{title, detail}`; inline `throw new Error(...)`
      // surfaces as `{message}`. Prefer the most specific text available.
      const e = (err ?? {}) as { message?: string; detail?: string; title?: string };
      error = e.detail ?? e.message ?? e.title ?? "Failed to create album";
    } finally {
      pending = false;
    }
  }

  function updateName(value: string) {
    name = value.slice(0, 200);
  }
</script>

<form onsubmit={submit} class="new-album-form">
  <label for="new-album-name">Name</label>
  <TextInput
    id="new-album-name"
    value={name}
    oninput={updateName}
    placeholder="Album name"
    invalid={error !== null}
    {...(error ? { ariaDescribedby: "new-album-error" } : {})}
    autofocus
    block
  />
  {#if error}<div id="new-album-error" class="error" role="alert">{error}</div>{/if}
  <div class="actions">
    {#if onCancel}<Button onclick={onCancel} disabled={pending}>Cancel</Button>{/if}
    <Button type="submit" tone="info" surface="solid" disabled={!valid || pending}>
      {pending ? "Creating…" : "Create"}
    </Button>
  </div>
</form>

<style>
  .new-album-form { display: flex; flex-direction: column; gap: 12px; }
  label { color: var(--text-secondary); font-size: var(--font-size-sm); }
  .error { color: var(--accent-red); font-size: 13px; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; }
</style>
