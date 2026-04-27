// Returns true when a keyboard event target is a form control or
// text-editing surface (INPUT, TEXTAREA, SELECT, or contenteditable
// element). Window-scoped key handlers should bail out for these so
// they don't steal characters from the user's typing, hijack
// Esc/dismiss behaviour from form controls, or fight a focused select
// for type-ahead navigation keys.
export function isEditableTarget(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false;
  return (
    t.tagName === "INPUT" ||
    t.tagName === "TEXTAREA" ||
    t.tagName === "SELECT" ||
    t.isContentEditable
  );
}
