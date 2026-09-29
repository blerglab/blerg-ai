// BoardFilterBar — client-side filter controls for the board view.
// Filters tickets by tag, priority, and size. All filters are multi-select;
// a ticket must match ALL active filter dimensions (AND), but within each
// dimension any selected value matches (OR).

export interface FilterState {
  tags: string[]
  priorities: string[]
  sizes: string[]
}

interface BoardFilterBarProps {
  availableTags: string[]
  availablePriorities: string[]
  availableSizes: string[]
  filter: FilterState
  onChange: (filter: FilterState) => void
}

function toggle(arr: string[], value: string): string[] {
  return arr.includes(value) ? arr.filter((v) => v !== value) : [...arr, value]
}

const PRIORITY_COLORS: Record<string, string> = {
  low: 'var(--lichen)',
  medium: 'var(--lichen)',
  high: 'var(--blaze)',
  urgent: 'var(--danger)',
}

export default function BoardFilterBar({
  availableTags,
  availablePriorities,
  availableSizes,
  filter,
  onChange,
}: BoardFilterBarProps) {
  const hasFilter =
    filter.tags.length > 0 || filter.priorities.length > 0 || filter.sizes.length > 0

  if (availableTags.length === 0 && availablePriorities.length === 0 && availableSizes.length === 0) {
    return null
  }

  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        flexWrap: 'wrap',
        gap: 6,
        padding: '8px 16px',
        borderBottom: '1px solid var(--basalt)',
        background: 'var(--basalt)',
      }}
    >
      {/* Tag filters */}
      {availableTags.map((tag) => {
        const active = filter.tags.includes(tag)
        return (
          <button
            key={tag}
            data-testid={`filter-tag-${tag}`}
            data-active={active ? 'true' : 'false'}
            onClick={() => onChange({ ...filter, tags: toggle(filter.tags, tag) })}
            style={{
              fontSize: '0.7rem',
              padding: '2px 8px',
              borderRadius: 4,
              border: active ? '1px solid var(--blaze)' : '1px solid var(--stone)',
              background: active ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--basalt)',
              color: active ? 'var(--amber)' : 'var(--fog)',
              cursor: 'pointer',
              fontFamily: 'inherit',
            }}
          >
            {tag}
          </button>
        )
      })}

      {/* Priority filters */}
      {availablePriorities.map((p) => {
        const active = filter.priorities.includes(p)
        const col = PRIORITY_COLORS[p] ?? 'var(--fog-dim)'
        return (
          <button
            key={p}
            data-testid={`filter-priority-${p}`}
            data-active={active ? 'true' : 'false'}
            onClick={() => onChange({ ...filter, priorities: toggle(filter.priorities, p) })}
            style={{
              fontSize: '0.7rem',
              padding: '2px 8px',
              borderRadius: 4,
              border: `1px solid ${active ? col : 'var(--stone)'}`,
              background: active ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--basalt)',
              color: active ? col : 'var(--fog)',
              cursor: 'pointer',
              fontFamily: 'inherit',
            }}
          >
            {p}
          </button>
        )
      })}

      {/* Size filters */}
      {availableSizes.map((sz) => {
        const active = filter.sizes.includes(sz)
        return (
          <button
            key={sz}
            data-testid={`filter-size-${sz}`}
            data-active={active ? 'true' : 'false'}
            onClick={() => onChange({ ...filter, sizes: toggle(filter.sizes, sz) })}
            style={{
              fontSize: '0.7rem',
              padding: '2px 8px',
              borderRadius: 4,
              border: active ? '1px solid var(--lichen)' : '1px solid var(--stone)',
              background: active ? 'color-mix(in srgb, var(--lichen) 22%, var(--basalt))' : 'var(--basalt)',
              color: active ? 'var(--lichen)' : 'var(--fog)',
              cursor: 'pointer',
              fontFamily: 'inherit',
            }}
          >
            {sz}
          </button>
        )
      })}

      {/* Clear all */}
      {hasFilter && (
        <button
          data-testid="filter-clear"
          onClick={() => onChange({ tags: [], priorities: [], sizes: [] })}
          style={{
            fontSize: '0.7rem',
            padding: '2px 8px',
            borderRadius: 4,
            border: '1px solid var(--stone)',
            background: 'none',
            color: 'var(--fog-dim)',
            cursor: 'pointer',
            fontFamily: 'inherit',
            marginLeft: 4,
          }}
        >
          clear
        </button>
      )}
    </div>
  )
}
