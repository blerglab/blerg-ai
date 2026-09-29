import { useCallback, useLayoutEffect, useRef, type DependencyList } from "react";

// How close to the bottom still counts as "following the conversation".
// Generous enough to survive fractional scroll heights (zoom, hidpi) and a
// nudge of the wheel, tight enough that a deliberate scroll-up sticks.
const NEAR_BOTTOM_PX = 48;

/**
 * useStickToBottom — chat auto-follow that yields to the reader.
 *
 * A live session posts events every few seconds; pinning the viewport on each
 * one makes it impossible to read back through history while the agent works.
 * So we only re-pin when the user was already parked at the bottom, and we
 * re-pin by writing `scrollTop` on the container itself — `scrollIntoView`
 * walks up and scrolls every ancestor scroller too, which yanks the whole page.
 *
 * "The reader scrolled up" describes one transcript in one DOM node, so it is
 * forgotten whenever either is replaced: a new `resetKey` (a different session)
 * or a remounted container both restore auto-follow. Otherwise a scroll-up in a
 * finished session would strand the next one at the top of its history.
 *
 * Attach `ref` to the scrolling element and `onScroll` to its scroll event,
 * pass the values that mean "new content" as `deps`, and call
 * `scrollToBottom()` for the cases that should always follow (the user sending
 * a message re-arms auto-follow no matter where they were reading).
 */
export function useStickToBottom(
  deps: DependencyList,
  opts: { resetKey?: unknown; threshold?: number } = {},
) {
  const { resetKey, threshold = NEAR_BOTTOM_PX } = opts;
  const elRef = useRef<HTMLDivElement | null>(null);
  const roRef = useRef<ResizeObserver | null>(null);
  const stuck = useRef(true); // start following; a scroll-up is what opts out

  const pin = useCallback(() => {
    const el = elRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, []);

  const ref = useCallback((node: HTMLDivElement | null) => {
    roRef.current?.disconnect();
    roRef.current = null;
    elRef.current = node;
    if (!node) return;
    // a freshly mounted node comes back at scrollTop 0 — the reader's place
    // died with the old node, so there is nothing left to protect: follow again
    stuck.current = true;
    node.scrollTop = node.scrollHeight;
    // the composer grows a line after send() and steals height from the
    // conversation; re-pin on any resize we are still following, or the view
    // ends up a line short of the bottom
    if (typeof ResizeObserver !== "undefined") {
      const ro = new ResizeObserver(() => { if (stuck.current) pin(); });
      ro.observe(node);
      roRef.current = ro;
    }
  }, [pin]);

  const onScroll = useCallback(() => {
    const el = elRef.current;
    if (!el) return;
    stuck.current = el.scrollHeight - el.scrollTop - el.clientHeight <= threshold;
  }, [threshold]);

  // a different conversation is a different thing to read — whatever the reader
  // decided about the last transcript says nothing about this one
  useLayoutEffect(() => {
    stuck.current = true;
    pin();
  }, [resetKey, pin]);

  // layout effect, not effect: pin after the new events are in the DOM but
  // before paint, so a followed conversation never shows a frame mid-jump
  useLayoutEffect(() => {
    if (stuck.current) pin();
    // The caller's list of "new content" values is the trigger by design, so
    // it cannot be a static array literal, and `pin` is a stable callback.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  const scrollToBottom = useCallback(() => {
    stuck.current = true;
    pin();
  }, [pin]);

  return { ref, onScroll, scrollToBottom };
}
