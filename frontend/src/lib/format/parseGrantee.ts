// parseGrantee turns "hub:user_id" into the API's object shape.
// Returns null if the input doesn't have exactly one colon, if either
// side is empty, or if either side exceeds 255 chars.
export type Grantee = { hub: string; user_id: string };

const MAX_PRINCIPAL_FIELD = 255;

export function parseGrantee(raw: string): Grantee | null {
  const trimmed = raw.trim();
  if (trimmed.length === 0) return null;
  if (/\s/.test(trimmed)) return null;
  const idx = trimmed.indexOf(":");
  if (idx <= 0 || idx === trimmed.length - 1) return null;
  if (trimmed.indexOf(":", idx + 1) !== -1) return null;
  const hub = trimmed.slice(0, idx);
  const user_id = trimmed.slice(idx + 1);
  if (hub.length > MAX_PRINCIPAL_FIELD) return null;
  if (user_id.length > MAX_PRINCIPAL_FIELD) return null;
  return { hub, user_id };
}
