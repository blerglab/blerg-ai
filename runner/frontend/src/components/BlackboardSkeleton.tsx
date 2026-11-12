import '../../../../packages/settings/InitialLoader.css'
export function BlackboardCountSkeleton() {
 return <span className="initial-skeleton-value blackboard-count-skeleton" aria-hidden="true" />
}
export default function BlackboardSkeleton() {
 return <section className="blackboard blackboard-skeleton" aria-label="Blackboard">
  <span className="initial-loader-label" role="status">Loading blackboard</span>
  <div className="blackboard-nav" aria-hidden="true">{[0, 1].map(i => <span className="initial-skeleton-value blackboard-skeleton-pill" key={i} />)}</div>
  <div className="blackboard-heading" aria-hidden="true"><span className="initial-skeleton-value blackboard-skeleton-title"/><BlackboardCountSkeleton /></div>
  <div className="blackboard-summary" aria-hidden="true"><span className="initial-skeleton-value blackboard-skeleton-bar"/><BlackboardCountSkeleton /></div>
  <div className="blackboard-filters" aria-hidden="true">{Array.from({length:8}, (_, i) => <span className="initial-skeleton-value blackboard-skeleton-pill" key={i}/>)}</div>
  <div className="blackboard-committed-groups" aria-hidden="true">{[0, 1, 2].map(i => <div className="blackboard-thrust blackboard-skeleton-tile" key={i}><BlackboardCountSkeleton/><span className="initial-skeleton-value blackboard-skeleton-title"/><span className="initial-skeleton-value blackboard-skeleton-bar"/><BlackboardCountSkeleton/><BlackboardCountSkeleton/></div>)}</div>
 </section>
}
