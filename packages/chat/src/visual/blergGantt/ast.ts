// BLG-471 §1/§2 (gantt): the declared gantt subset's own AST — never Mermaid's. Mirrors
// blergSequence/ast.ts's own framing: one shape for everything this slice draws, nothing a
// construct outside the declared grammar (`excludes`, `todayMarker`, `click`, any dateFormat
// other than `YYYY-MM-DD`) would need — that construct is a reason for `parseGantt` to return
// null (the failure rule), not a shape this type needs to represent.
export interface GanttTask {
  id: string
  /** Decoded (blergFlow/label.ts) — never raw Mermaid source. */
  label: string
  section: string
  start: Date
  end: Date
  milestone: boolean
  crit: boolean
  active: boolean
  done: boolean
  /** Ids of tasks this one starts `after` (design doc's declared subset) — always ids already
   *  declared earlier in `tasks`, resolved by parse.ts before this shape exists. */
  dependsOn: readonly string[]
}

export interface GanttGraph {
  title: string | null
  /** Author's raw `axisFormat` token (e.g. `%m-%d`), or null for the default. draw.ts formats
   *  ticks with it; an unrecognized specifier is left literal, never a parse failure — axisFormat
   *  is cosmetic, not structural. */
  axisFormat: string | null
  /** Section names in first-appearance order — the lane names the gantt's sticky column pins. */
  sections: readonly string[]
  /** Source order — the order dependencies must already have been declared in. */
  tasks: readonly GanttTask[]
}
