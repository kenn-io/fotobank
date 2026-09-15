<script lang="ts">
  import { api } from "../lib/api/client";
  import type { EffectiveResponse, ApplyResponse, Result as ProbeResult } from "../lib/api/generated/models";

  type FieldValue = string | number | boolean;
  type Section = "master" | "vision" | "tag" | "caption" | "embed";
  type FieldKind = "text" | "number" | "checkbox";

  type Field = {
    key: string;
    label: string;
    kind: FieldKind;
  };

  type SectionDef = {
    id: Exclude<Section, "master">;
    title: string;
    fields: Field[];
    testable: boolean;
  };

  const masterField: Field = { key: "ai.enabled", label: "AI enabled", kind: "checkbox" };
  const sections: SectionDef[] = [
    {
      id: "vision",
      title: "Vision",
      testable: true,
      fields: [
        { key: "ai.vision.endpoint", label: "Endpoint", kind: "text" },
        { key: "ai.vision.api_key_env", label: "API key env", kind: "text" },
      ],
    },
    {
      id: "tag",
      title: "Tag",
      testable: true,
      fields: [
        { key: "ai.tag.enabled", label: "Enabled", kind: "checkbox" },
        { key: "ai.tag.model", label: "Model", kind: "text" },
      ],
    },
    {
      id: "caption",
      title: "Caption",
      testable: true,
      fields: [
        { key: "ai.caption.enabled", label: "Enabled", kind: "checkbox" },
        { key: "ai.caption.model", label: "Model", kind: "text" },
      ],
    },
    {
      id: "embed",
      title: "Embed",
      testable: true,
      fields: [
        { key: "ai.embed.enabled", label: "Enabled", kind: "checkbox" },
        { key: "ai.embed.endpoint", label: "Endpoint", kind: "text" },
        { key: "ai.embed.api_key_env", label: "API key env", kind: "text" },
        { key: "ai.embed.model", label: "Model", kind: "text" },
        { key: "ai.embed.dimension", label: "Dimension", kind: "number" },
        { key: "ai.embed.input_edge", label: "Input edge", kind: "number" },
      ],
    },
  ];

  let data = $state<EffectiveResponse | null>(null);
  let forms = $state<Record<Section, Record<string, FieldValue>>>({
    master: {},
    vision: {},
    tag: {},
    caption: {},
    embed: {},
  });
  let loading = $state(true);
  let busy = $state<string | null>(null);
  let error = $state<string | null>(null);
  let probes = $state<Partial<Record<Section, ProbeResult>>>({});
  let generationMessage = $state<string | null>(null);
  let confirmEmbedApply = $state(false);

  void load();

  async function load(): Promise<void> {
    loading = true;
    error = null;
    const { data: body, error: apiError } = await api.adminSettingsGet();
    if (apiError || !body) {
      error = "Unable to load admin settings.";
      loading = false;
      return;
    }
    data = body;
    forms = {
      master: valuesFor([masterField], body.effective),
      vision: valuesFor(sections[0]!.fields, body.effective),
      tag: valuesFor(sections[1]!.fields, body.effective),
      caption: valuesFor(sections[2]!.fields, body.effective),
      embed: valuesFor(sections[3]!.fields, body.effective),
    };
    loading = false;
  }

  function valuesFor(fields: Field[], source: Record<string, unknown>): Record<string, FieldValue> {
    const out: Record<string, FieldValue> = {};
    for (const f of fields) out[f.key] = normalizeValue(source[f.key], f.kind);
    return out;
  }

  function normalizeValue(v: unknown, kind: FieldKind): FieldValue {
    if (kind === "checkbox") return v === true;
    if (kind === "number") return typeof v === "number" ? v : Number(v ?? 0);
    return typeof v === "string" ? v : "";
  }

  function setValue(section: Section, key: string, kind: FieldKind, raw: Event): void {
    const target = raw.currentTarget as HTMLInputElement;
    const value = kind === "checkbox" ? target.checked : kind === "number" ? Number(target.value) : target.value;
    forms = { ...forms, [section]: { ...forms[section], [key]: value } };
  }

  function isDirty(section: Section): boolean {
    const current = data;
    if (!current) return false;
    return Object.entries(forms[section]).some(([key, value]) => value !== normalizeValue(current.effective[key], keyKind(key)));
  }

  function keyKind(key: string): FieldKind {
    const all = [masterField, ...sections.flatMap((s) => s.fields)];
    return all.find((f) => f.key === key)?.kind ?? "text";
  }

  function status(section: Section): string {
    if (isDirty(section)) return "dirty";
    if (!data) return "clean";
    const keys = Object.keys(forms[section]);
    return keys.some((k) => data?.overrides[k]) ? "overridden" : "clean";
  }

  function discard(section: Section): void {
    if (!data) return;
    const fields = section === "master" ? [masterField] : sections.find((s) => s.id === section)?.fields ?? [];
    forms = { ...forms, [section]: valuesFor(fields, data.effective) };
    error = null;
  }

  async function apply(section: Section): Promise<void> {
    if (section === "embed" && embedGenerationChanged()) {
      confirmEmbedApply = true;
      return;
    }
    await applyNow(section);
  }

  async function applyNow(section: Section): Promise<void> {
    busy = `apply-${section}`;
    error = null;
    try {
      const apiSection = section;
      const { data: body, error: apiError } = await api.adminSettingsApplySection(apiSection, { values: forms[section] });
      if (apiError || !body) throw new Error(detail(apiError) || "Apply failed.");
      mergeApply(section, body);
      if (body.generation_id !== undefined) generationMessage = `Building generation #${body.generation_id}`;
      confirmEmbedApply = false;
    } catch (e) {
      error = e instanceof Error ? e.message : "Apply failed.";
    } finally {
      busy = null;
    }
  }

  function mergeApply(section: Section, body: ApplyResponse): void {
    if (!data) return;
    data = {
      ...data,
      effective: { ...data.effective, ...body.effective },
    };
    discard(section);
  }

  async function resetSection(section: Section): Promise<void> {
    busy = `reset-${section}`;
    error = null;
    try {
      const apiSection = section;
      const { data: body, error: apiError } = await api.adminSettingsResetSection(apiSection);
      if (apiError || !body) throw new Error(detail(apiError) || "Reset failed.");
      mergeApply(section, body);
      if (body.generation_id !== undefined) generationMessage = `Building generation #${body.generation_id}`;
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : "Reset failed.";
    } finally {
      busy = null;
    }
  }

  async function resetKey(section: Section, key: string): Promise<void> {
    busy = `reset-${key}`;
    error = null;
    try {
      const { data: body, error: apiError } = await api.adminSettingsResetKey(key);
      if (apiError || !body) throw new Error(detail(apiError) || "Reset failed.");
      mergeApply(section, body);
      if (body.generation_id !== undefined) generationMessage = `Building generation #${body.generation_id}`;
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : "Reset failed.";
    } finally {
      busy = null;
    }
  }

  async function testSection(section: Section): Promise<void> {
    busy = `test-${section}`;
    error = null;
    try {
      if (section === "embed") {
        const { data: body, error: apiError } = await api.adminSettingsTestEmbed({
            endpoint: String(forms.embed["ai.embed.endpoint"] ?? ""),
            api_key_env: String(forms.embed["ai.embed.api_key_env"] ?? ""),
            model: String(forms.embed["ai.embed.model"] ?? ""),
            dimension: Number(forms.embed["ai.embed.dimension"] ?? 0),
          });
        if (apiError || !body) throw new Error(detail(apiError) || "Probe failed.");
        probes = { ...probes, embed: body };
      } else {
        const modelKey = section === "caption" ? "ai.caption.model" : "ai.tag.model";
        const { data: body, error: apiError } = await api.adminSettingsTestVision({
            endpoint: String(forms.vision["ai.vision.endpoint"] ?? ""),
            api_key_env: String(forms.vision["ai.vision.api_key_env"] ?? ""),
            model: String((forms[section] ?? forms.tag)[modelKey] ?? forms.tag["ai.tag.model"] ?? ""),
          });
        if (apiError || !body) throw new Error(detail(apiError) || "Probe failed.");
        probes = { ...probes, [section]: body };
      }
    } catch (e) {
      error = e instanceof Error ? e.message : "Probe failed.";
    } finally {
      busy = null;
    }
  }

  function canTest(section: Section): boolean {
    if (section === "embed") {
      return String(forms.embed["ai.embed.endpoint"] ?? "") !== "" &&
        String(forms.embed["ai.embed.model"] ?? "") !== "" &&
        Number(forms.embed["ai.embed.dimension"] ?? 0) > 0;
    }
    return String(forms.vision["ai.vision.endpoint"] ?? "") !== "";
  }

  function embedGenerationChanged(): boolean {
    if (!data) return false;
    for (const key of ["ai.embed.model", "ai.embed.dimension", "ai.embed.input_edge"]) {
      if (forms.embed[key] !== normalizeValue(data.effective[key], keyKind(key))) return true;
    }
    return false;
  }

  function detail(apiError: unknown): string | null {
    if (apiError && typeof apiError === "object" && "detail" in apiError) {
      const d = (apiError as { detail?: unknown }).detail;
      return typeof d === "string" ? d : null;
    }
    return null;
  }

  function fieldSource(key: string): string {
    if (!data) return "";
    const meta = data.overrides[key];
    if (!meta) return "from config.toml";
    return `edited ${new Date(meta.updated_at).toLocaleString()}`;
  }

  function apiKeyEnvText(section: Section): string | null {
    const st = data?.api_key_env_status[`ai.${section}.api_key_env`];
    if (!st) return null;
    if (!st.required) return "No API key required";
    return `$${st.name} ${st.is_set ? "is set" : "is unset"}`;
  }

  function apiKeyEnvIsSet(section: Section): boolean {
    return data?.api_key_env_status[`ai.${section}.api_key_env`]?.is_set === true;
  }
</script>

<section class="admin-ai">
  <header class="page-header">
    <div>
      <h2>AI Settings</h2>
      <p>Server-wide runtime configuration.</p>
    </div>
    <a href="/settings/ai">AI status</a>
  </header>

  {#if loading}
    <p class="muted">Loading…</p>
  {:else if error}
    <p class="error">{error}</p>
  {/if}

  {#if data}
    <article class="settings-section">
      <header>
        <div>
          <h3>Master Toggle</h3>
          <span class="status" data-status={status("master")}>{status("master")}</span>
        </div>
        <div class="actions">
          <button type="button" onclick={() => discard("master")} disabled={!isDirty("master")}>Discard</button>
          <button type="button" onclick={() => resetSection("master")} disabled={busy === "reset-master"}>Reset</button>
          <button type="button" onclick={() => apply("master")} disabled={!isDirty("master") || busy === "apply-master"}>Apply</button>
        </div>
      </header>
      <div class="field-row">
        <label>
          <span>{masterField.label}</span>
          <input type="checkbox" checked={Boolean(forms.master[masterField.key])} onchange={(e) => setValue("master", masterField.key, masterField.kind, e)} />
        </label>
        <small>{fieldSource(masterField.key)}</small>
        {#if data.overrides[masterField.key]}
          <button type="button" class="icon" title="Reset key" onclick={() => resetKey("master", masterField.key)}>↺</button>
        {/if}
      </div>
    </article>

    {#each sections as section (section.id)}
      <article class="settings-section" data-paused={forms.master["ai.enabled"] !== true}>
        <header>
          <div>
            <h3>{section.title}</h3>
            <span class="status" data-status={status(section.id)}>{status(section.id)}</span>
          </div>
          <div class="actions">
            {#if section.testable}
              <button type="button" onclick={() => testSection(section.id)} disabled={!canTest(section.id) || busy === `test-${section.id}`}>Test</button>
            {/if}
            <button type="button" onclick={() => discard(section.id)} disabled={!isDirty(section.id)}>Discard</button>
            <button type="button" onclick={() => resetSection(section.id)} disabled={busy === `reset-${section.id}`}>Reset section</button>
            <button type="button" onclick={() => apply(section.id)} disabled={!isDirty(section.id) || busy === `apply-${section.id}`}>Apply</button>
          </div>
        </header>
        {#each section.fields as field (field.key)}
          <div class="field-row">
            <label>
              <span>{field.label}</span>
              {#if field.kind === "checkbox"}
                <input type="checkbox" checked={Boolean(forms[section.id][field.key])} onchange={(e) => setValue(section.id, field.key, field.kind, e)} />
              {:else}
                <input type={field.kind === "number" ? "number" : "text"} value={String(forms[section.id][field.key] ?? "")} oninput={(e) => setValue(section.id, field.key, field.kind, e)} />
              {/if}
            </label>
            <small>{fieldSource(field.key)}</small>
            {#if data.overrides[field.key]}
              <button type="button" class="icon" title="Reset key" onclick={() => resetKey(section.id, field.key)} disabled={busy === `reset-${field.key}`}>↺</button>
            {/if}
          </div>
        {/each}
        {#if apiKeyEnvText(section.id)}
          <p class="env-status" data-set={apiKeyEnvIsSet(section.id)}>{apiKeyEnvText(section.id)}</p>
        {/if}
        {#if probes[section.id]}
          {@const p = probes[section.id]!}
          <p class="probe" data-ok={p.ok}>{p.classification}: {p.detail} ({p.latency_ms}ms)</p>
          {#if p.warnings?.length}
            <ul class="warnings">{#each p.warnings as w}<li>{w}</li>{/each}</ul>
          {/if}
        {/if}
      </article>
    {/each}

    {#if generationMessage}
      <p class="generation">{generationMessage}</p>
    {/if}
  {/if}

  {#if confirmEmbedApply && data}
    <div class="modal-backdrop" role="presentation">
      <div class="modal" role="dialog" aria-labelledby="embed-confirm-title">
        <h3 id="embed-confirm-title">Create a new embedding generation?</h3>
        <dl>
          <dt>Current</dt>
          <dd>
            {#if data.current_embed_generation}
              #{data.current_embed_generation.id} · {data.current_embed_generation.model} · {data.current_embed_generation.dimension}d · {data.current_embed_generation.state}
            {:else}
              No active generation
            {/if}
          </dd>
          <dt>Proposed</dt>
          <dd>{forms.embed["ai.embed.model"]} · {forms.embed["ai.embed.dimension"]}d · edge {forms.embed["ai.embed.input_edge"]}</dd>
        </dl>
        <p>A rebuild can take hours on large libraries. The old generation stays active until the new one promotes.</p>
        <div class="actions">
          <button type="button" onclick={() => { confirmEmbedApply = false; }}>Cancel</button>
          <button type="button" onclick={() => applyNow("embed")} disabled={busy === "apply-embed"}>Confirm apply</button>
        </div>
      </div>
    </div>
  {/if}
</section>

<style>
  .admin-ai { padding: 20px; max-width: 860px; }
  .page-header, .settings-section > header { display: flex; justify-content: space-between; gap: 16px; align-items: flex-start; }
  .page-header h2, .settings-section h3 { margin: 0; }
  .page-header p { margin: 4px 0 0; color: var(--text-muted); font-size: 12px; }
  .settings-section { border-top: 1px solid var(--border-default); padding: 18px 0; }
  .settings-section[data-paused="true"] { opacity: 0.72; }
  .actions { display: flex; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }
  button, input { font: inherit; }
  button { border: 1px solid var(--border-default); background: var(--bg-surface); border-radius: 6px; padding: 6px 10px; cursor: pointer; }
  button:disabled { opacity: 0.45; cursor: default; }
  .field-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 6px 12px; align-items: center; margin-top: 12px; }
  .field-row label { display: grid; grid-template-columns: 150px minmax(0, 1fr); gap: 12px; align-items: center; }
  .field-row input[type="text"], .field-row input[type="number"] { width: 100%; min-width: 0; border: 1px solid var(--border-default); border-radius: 6px; padding: 7px 8px; background: var(--bg-surface); color: var(--text-primary); }
  .field-row small { grid-column: 1 / -1; color: var(--text-muted); font-size: 11px; }
  .icon { padding: 4px 8px; }
  .status { display: inline-block; margin-top: 6px; font-size: 11px; color: var(--text-muted); text-transform: uppercase; }
  .status[data-status="dirty"] { color: #b45309; }
  .status[data-status="overridden"] { color: var(--accent-blue); }
  .error { color: var(--accent-red); }
  .muted, .env-status { color: var(--text-muted); font-size: 12px; }
  .env-status[data-set="true"], .probe[data-ok="true"] { color: var(--accent-green); }
  .probe[data-ok="false"] { color: var(--accent-red); }
  .warnings { color: #b45309; font-size: 12px; }
  .generation { border: 1px solid var(--border-default); padding: 10px; border-radius: 6px; }
  .modal-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,.35); display: grid; place-items: center; padding: 20px; z-index: 20; }
  .modal { max-width: 520px; width: min(100%, 520px); background: var(--bg-surface); border: 1px solid var(--border-default); border-radius: 8px; padding: 18px; box-shadow: 0 12px 40px rgba(0,0,0,.25); }
  .modal dl { display: grid; grid-template-columns: 90px 1fr; gap: 8px; }
  .modal dt { color: var(--text-muted); }
  @media (max-width: 640px) {
    .page-header, .settings-section > header { display: block; }
    .actions { justify-content: flex-start; margin-top: 10px; }
    .field-row { grid-template-columns: 1fr; }
    .field-row label { grid-template-columns: 1fr; }
  }
</style>
