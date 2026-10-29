import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import type { Column } from "./api";
import { BoardFilter, countLabel, isFiltering } from "./boardFilter";

export interface ColumnCount { column: Column; shown: number; total: number }

// BoardFilterBar: the board's sticky strip. On a phone it holds the column tabs (one column shows
// at a time) and, under them, the full-width search and the active filters when there are any.
// On a desktop the search and filters live in the toolbar and this strip only carries the
// screen-reader announcement. `applied` is what the cards and counts follow.
export function BoardFilterBar({ counts, activeColumn, onPickColumn, applied, children }: {
  counts: ColumnCount[];
  activeColumn: string | null;
  onPickColumn: (id: string) => void;
  applied: BoardFilter;
  children?: ReactNode; // the phone's search row and active filters
}) {
  const filtering = isFiltering(applied);
  const tabsRef = useRef<HTMLDivElement>(null);
  // keep the picked tab in view when the strip scrolls sideways
  useEffect(() => {
    tabsRef.current?.querySelector<HTMLElement>("[aria-selected='true']")
      ?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }, [activeColumn]);

  // Tabs pattern: one Tab stop (the picked tab); arrows, Home and End move and pick.
  const onTabKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const i = counts.findIndex((c) => c.column.id === activeColumn);
    const last = counts.length - 1;
    const to = e.key === "ArrowRight" ? (i + 1) % counts.length
      : e.key === "ArrowLeft" ? (i - 1 + counts.length) % counts.length
      : e.key === "Home" ? 0 : e.key === "End" ? last : -1;
    if (to < 0 || !counts[to]) return;
    e.preventDefault();
    onPickColumn(counts[to].column.id);
    document.getElementById(`tab-${counts[to].column.id}`)?.focus();
  };

  // What the filter left, said to a screen reader once typing settles.
  const shown = counts.reduce((n, c) => n + c.shown, 0);
  const total = counts.reduce((n, c) => n + c.total, 0);
  const summary = filtering ? `${shown} of ${total} card${total === 1 ? "" : "s"} shown` : "";
  const [said, setSaid] = useState("");
  const wasFiltering = useRef(false);
  useEffect(() => {
    const cleared = wasFiltering.current && !filtering;
    wasFiltering.current = filtering;
    if (!filtering && !cleared) return;
    const t = setTimeout(() => setSaid(summary || `All ${total} cards shown`), 700);
    return () => clearTimeout(t);
    // Only a filter transition announces "All". Unfiltered count changes must neither repeat
    // that announcement nor cancel one waiting to be spoken; total is captured at Clear.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- summary tracks every filtered count; unfiltered counts are intentionally ignored
  }, [summary]);

  return (
    <div className="board-sticky">
      <div className="column-tabs" role="tablist" aria-label="Columns" ref={tabsRef} onKeyDown={onTabKey}>
        {counts.map(({ column, shown, total }) => {
          const on = column.id === activeColumn;
          return (
            // button-guard-ignore: column tabs show only under the phone media query, where .column-tab (styles.css) styles them.
            <button
              key={column.id} role="tab" type="button"
              id={`tab-${column.id}`} aria-controls={`column-${column.id}`}
              aria-selected={on} tabIndex={on ? 0 : -1}
              className={`column-tab${on ? " on" : ""}`}
              onClick={() => onPickColumn(column.id)}
            >
              <span className="name">{column.name}</span>
              <span className="count">{countLabel(shown, total, filtering)}</span>
            </button>
          );
        })}
      </div>
      {children}
      <div className="sr-only" aria-live="polite" role="status">{said}</div>
    </div>
  );
}
