// cn joins classnames, filtering falsy values. Tiny replacement for the
// `clsx` package — every primitive uses it so a typo in one component
// doesn't quietly emit `undefined` into the DOM.
export function cn(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ')
}
