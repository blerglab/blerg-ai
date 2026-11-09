import { Children, isValidElement, useState, type ReactElement, type ReactNode } from 'react'

function text(node: ReactNode): string {
  if (node == null || typeof node === 'boolean') return ''
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (isValidElement<{ children?: ReactNode }>(node)) return text(node.props.children)
  return Children.toArray(node).map(text).join('')
}

// Keep the existing table presentation. Rows supply unformatted sort values where
// displayed duration/cost abbreviations would otherwise change numeric ordering.
export default function InsightsTable({ children, className = 'grid' }: { children: ReactNode; className?: string }) {
  const [sort, setSort] = useState<{ column: number; descending: boolean } | null>(null)
  const parts = Children.toArray(children) as ReactElement<{ children: ReactNode }>[]
  return <table className={className}>{parts.map(part => {
    if (part.type === 'thead') {
      const row = Children.only(part.props.children) as ReactElement<{ children: ReactNode }>
      return <thead key="head"><tr>{Children.toArray(row.props.children).map((cell, column) => {
        const th = cell as ReactElement<{ children: ReactNode; className?: string }>
        if (!text(th.props.children)) return <th key={column} />
        return <th key={column} className={th.props.className} aria-sort={sort?.column === column ? sort.descending ? 'descending' : 'ascending' : 'none'}><button className="insights-sort" type="button" onClick={() => setSort({ column, descending: sort?.column === column ? !sort.descending : false })}>{th.props.children}{sort?.column === column ? sort.descending ? ' ↓' : ' ↑' : ''}</button></th>
      })}</tr></thead>
    }
    if (part.type !== 'tbody') return part
    const rows = Children.toArray(part.props.children) as ReactElement<{ children: ReactNode; 'data-sort'?: (string | number | null)[] }>[]
    if (sort) rows.sort((a, b) => {
      const value = (row: typeof a) => row.props['data-sort']?.[sort.column] ?? text((Children.toArray(row.props.children)[sort.column] as ReactElement<{ children: ReactNode }>).props.children)
      const x = value(a), y = value(b)
      const order = typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y), undefined, { numeric: true })
      return sort.descending ? -order : order
    })
    return <tbody key="body">{rows}</tbody>
  })}</table>
}
