// DataTable: the one table style for CSV and TSV. The viewer shows the whole (capped) table with a
// sticky header; the inline preview shows the first rows and columns of the same table, in the
// same borders, header, padding and zebra rows, with each cell cut to one line. Cells are text.
export default function DataTable({ rows, preview = false, testId }: { rows: string[][]; preview?: boolean; testId: string }) {
  const [head, ...rest] = rows
  return (
    <div className={`data-table-wrap${preview ? ' is-preview' : ''}`} tabIndex={preview ? undefined : 0}>
      <table className={`data-table${preview ? ' is-preview' : ''}`} data-testid={testId}>
        <thead>
          <tr>{head.map((c, i) => <th key={i} scope="col">{c}</th>)}</tr>
        </thead>
        <tbody>
          {rest.map((r, i) => <tr key={i}>{r.map((c, j) => <td key={j}>{c}</td>)}</tr>)}
        </tbody>
      </table>
    </div>
  )
}
