// Returns true when a keyboard event target is a text-editing surface
// (INPUT, TEXTAREA, or contenteditable element). Window-scoped key
// handlers should bail out for these so they don't steal characters
// from the user's typing or hijack Esc/dismiss behaviour from form
// controls.
export function isEditableTarget(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false;
  return t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable;
}
