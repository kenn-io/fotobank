// normalizeForSearch lowercases and trims a string for case-insensitive
// substring matching against album names. Uses locale-independent
// `toLowerCase` so a Turkish-locale runtime doesn't fold "Italy" to
// "ıtaly" (dotless i) and stop matching a query the user types as
// "italy" on a US keyboard. The corpus is small (typically <100
// albums) so NFC normalization isn't needed — Unicode equivalence
// pitfalls are unlikely in practice for album names users type in.
export function normalizeForSearch(s: string): string {
  return s.trim().toLowerCase();
}
