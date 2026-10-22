import { useEffect, useId, useRef, useState } from "react";
import { useAnchored } from "./anchor";
import { activeFilters, BoardFilter, Chip, Facet, fold, isFiltering, toggleChip, emptyFilter } from "./boardFilter";

const facetLabel: Record<Facet, string> = { type: "Type", priority: "Priority", tag: "Tag" };
const sections: Facet[] = ["type", "priority", "tag"];

// FilterMenu: the toolbar's Filter button. It opens the type, priority and tag choices: a popover
// under the button on a desktop, a bottom sheet on a phone. Each choice shows how many cards it
// would leave; tags are sorted by count and have their own search.
export function FilterMenu({ chips, filter, onFilter, sheet }: {
  chips: Chip[];
  filter: BoardFilter;
  onFilter: (f: BoardFilter) => void;
  sheet: boolean; // a phone: a bottom sheet instead of a popover
}) {
  const [open, setOpen] = useState(false);
  const [tagQ, setTagQ] = useState("");
  const btnRef = useRef<HTMLButtonElement>(null);
  const popRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const on = activeFilters(filter).length;
  const popStyle = useAnchored(btnRef, open && !sheet, (vw) => Math.min(Math.max(520, vw * 0.4), 1400));

  const close = (refocus = true) => { setOpen(false); setTagQ(""); if (refocus) btnRef.current?.focus(); };
  // The popover closes on a click outside it and when focus leaves it. The phone's sheet is modal:
  // Tab cycles inside it, focus that escapes is brought back, and a tap on the backdrop closes it
  // with focus back on the Filter button. Escape closes either.
  useEffect(() => {
    if (!open) return;
    const outside = (t: EventTarget | null) =>
      t instanceof Node && !popRef.current?.contains(t) && !btnRef.current?.contains(t);
    const shut = (refocus: boolean) => { setOpen(false); setTagQ(""); if (refocus) btnRef.current?.focus(); };
    const onDown = (e: PointerEvent) => { if (outside(e.target)) shut(sheet); };
    const onFocus = (e: FocusEvent) => {
      if (!outside(e.target)) return;
      if (sheet) focusables(popRef.current)[0]?.focus();
      else shut(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.stopPropagation(); shut(true); return; }
      if (e.key !== "Tab" || !sheet) return;
      const all = focusables(popRef.current);
      if (all.length === 0) return;
      const first = all[0], last = all[all.length - 1];
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    };
    document.addEventListener("pointerdown", onDown);
    document.addEventListener("focusin", onFocus);
    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("pointerdown", onDown);
      document.removeEventListener("focusin", onFocus);
      document.removeEventListener("keydown", onKey, true);
    };
  }, [open, sheet]);
  useEffect(() => {
    if (open) popRef.current?.querySelector<HTMLElement>("button.filter-opt, input")?.focus();
  }, [open]);

  const tagMatch = fold(tagQ.trim());
  const body = (
    <>
      {sections.map((facet) => {
        let list = chips.filter((c) => c.facet === facet);
        if (facet === "tag") {
          // by count, then name: a choice does not jump when it is picked
          list = [...list].sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
          if (tagMatch) list = list.filter((c) => fold(c.value).includes(tagMatch));
        }
        if (list.length === 0 && facet !== "tag") return null;
        return (
          <section key={facet} className={`filter-section ${facet}`}>
            <h4>{facetLabel[facet]}</h4>
            {facet === "tag" && (
              <input type="search" className="filter-tag-q" placeholder="Find a tag" aria-label="Find a tag"
                value={tagQ} onChange={(e) => setTagQ(e.target.value)} />
            )}
            <div className="filter-opts" role="group" aria-label={facetLabel[facet]}>
              {list.map((c) => (
                <button key={c.value} type="button" className={`filter-opt${c.on ? " on" : ""}`}
                  aria-pressed={c.on}
                  aria-label={`${facetLabel[c.facet]} ${c.value}, ${c.count} card${c.count === 1 ? "" : "s"}`}
                  onClick={() => onFilter(toggleChip(filter, c.facet, c.value))}>
                  {c.on && <span aria-hidden="true">✓ </span>}{c.value} <span className="n">{c.count}</span>
                </button>
              ))}
              {facet === "tag" && list.length === 0 && <span className="dimtext">{tagMatch ? "No tag matches." : "No tags."}</span>}
            </div>
          </section>
        );
      })}
    </>
  );

  return (
    <span className="filter-menu">
      <button ref={btnRef} type="button" className={`btn ghost small filter-btn${on > 0 ? " on" : ""}`}
        aria-haspopup="dialog" aria-expanded={open}
        aria-label={on > 0 ? `Filter, ${on} on` : "Filter"}
        onClick={() => (open ? close() : setOpen(true))}>
        Filter{on > 0 && <span className="filter-count">{on}</span>}
      </button>
      {open && !sheet && (
        <div ref={popRef} className="filter-pop" role="dialog" aria-labelledby={titleId} style={popStyle}>
          <h3 id={titleId} className="sr-only">Filter cards</h3>
          {body}
        </div>
      )}
      {open && sheet && (
        <div className="filter-sheet-backdrop">
          <div ref={popRef} className="filter-sheet" role="dialog" aria-modal="true" aria-labelledby={titleId}>
            <div className="filter-sheet-head">
              <h3 id={titleId}>Filter cards</h3>
              <button type="button" className="btn ghost small" disabled={on === 0}
                onClick={() => onFilter({ ...emptyFilter, q: filter.q })}>Reset</button>
              <button type="button" className="btn small" onClick={() => close()}>Done</button>
            </div>
            <div className="filter-sheet-body">{body}</div>
          </div>
        </div>
      )}
    </span>
  );
}

// ActiveFilters: only what is on, as small removable chips ("search: auth ×", "type: bug ×",
// "0.4.5 ×"), and Clear. Nothing at all while no filter is on.
export function ActiveFilters({ filter, onFilter }: { filter: BoardFilter; onFilter: (f: BoardFilter) => void }) {
  if (!isFiltering(filter)) return null;
  const q = filter.q.trim();
  return (
    <span className="active-filters" role="group" aria-label="Active filters">
      {/* search text that filters the board is a filter too: always visible, reload included */}
      {q && (
        <button type="button" className="active-chip search"
          aria-label={`Remove search: ${q}`} onClick={() => onFilter({ ...filter, q: "" })}>
          search: {q} <span aria-hidden="true">×</span>
        </button>
      )}
      {activeFilters(filter).map((a) => (
        <button key={`${a.facet}:${a.value}`} type="button" className="active-chip"
          aria-label={`Remove ${a.label}`} onClick={() => onFilter(toggleChip(filter, a.facet, a.value))}>
          {a.label} <span aria-hidden="true">×</span>
        </button>
      ))}
      <button type="button" className="active-clear" onClick={() => onFilter(emptyFilter)}>Clear</button>
    </span>
  );
}

function focusables(root: HTMLElement | null): HTMLElement[] {
  if (!root) return [];
  return [...root.querySelectorAll<HTMLElement>("button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex='-1'])")];
}
