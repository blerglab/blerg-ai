import { useLayoutEffect, useState, type CSSProperties, type RefObject } from "react";

// useAnchored: a fixed-position box under `anchor`, as wide as `width(viewportWidth)` but never
// past the window: it starts at the anchor's left edge and shifts left when it would overflow.
// Fixed, so no scrolling or clipping ancestor can cut it off. Follows resizes and scrolls.
export function useAnchored(anchor: RefObject<HTMLElement | null>, open: boolean, width: (vw: number) => number): CSSProperties {
  const [style, setStyle] = useState<CSSProperties>({ position: "fixed", visibility: "hidden" });
  useLayoutEffect(() => {
    if (!open) return;
    const place = () => {
      const el = anchor.current;
      if (!el) return;
      const r = el.getBoundingClientRect();
      const vw = document.documentElement.clientWidth;
      const w = Math.min(width(vw), vw - 16);
      const left = Math.max(8, Math.min(r.left, vw - w - 8));
      setStyle({ position: "fixed", top: r.bottom + 6, left, width: w, maxHeight: `calc(100dvh - ${Math.round(r.bottom + 18)}px)` });
    };
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => { window.removeEventListener("resize", place); window.removeEventListener("scroll", place, true); };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `width` is a pure sizing rule
  }, [anchor, open]);
  return style;
}
