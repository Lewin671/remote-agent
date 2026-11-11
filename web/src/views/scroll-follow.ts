/** Where the chat's scroller stood at a scroll event. */
export type Scrolled = { top: number; height: number; client: number };

/** Less than this from the end counts as being at the end (a line or so). */
export const NEAR_END = 60;

/**
 * Whether the chat keeps following its end after a scroll event.
 *
 * Within `NEAR_END` of the end it follows. Further up it stops when the
 * reader scrolled (a wheel, a touch, a key or the bar within the last second),
 * or when the top moved up while the content and the column kept their size:
 * nothing but a scroll explains that, whatever made it (find in page, a key
 * pressed with the focus elsewhere, a script). Scroll events that come with a
 * change of height (the virtual list measuring rows, older records joining
 * above, the input growing) leave it as it was.
 */
export function followsEnd(was: boolean, last: Scrolled | undefined, now: Scrolled, readerAt: number, at: number): boolean {
  if (now.height - now.top - now.client < NEAR_END) return true;
  if (at - readerAt < 1000) return false;
  const up = last !== undefined && last.height === now.height && last.client === now.client && now.top < last.top - 1;
  return up ? false : was;
}
