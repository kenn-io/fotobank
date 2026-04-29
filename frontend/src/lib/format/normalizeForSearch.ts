// normalizeForSearch lowercases (locale-aware) and trims a string for
// case-insensitive substring matching against album names. The corpus
// is small (typically <100 albums) so no NFC normalization is needed
// — Unicode equivalence pitfalls are unlikely in practice for album
// names users type in.
export function normalizeForSearch(s: string): string {
  return s.trim().toLocaleLowerCase();
}
