import '../../../../packages/settings/InitialLoader.css'

// The BoardSkeleton bars, at the dimensions of a question, answer and chart.
export default function InsightsSkeleton() {
 return <div className="insights-skeleton">
  <span className="initial-loader-label" role="status">Loading insights…</span>
  <div aria-hidden="true">
   <h1><span className="initial-skeleton-value" /></h1>
   <p><span className="initial-skeleton-value" /></p>
   <div className="insights-skeleton-chart">{Array.from({length:6},(_,i)=><div className="initial-skeleton-row" key={i}><span className="initial-skeleton-key"/><span className="initial-skeleton-value"/></div>)}</div>
  </div>
 </div>
}
