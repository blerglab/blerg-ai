import { BoardColumn } from './BoardPresentation'
import '../settings/InitialLoader.css'

/** The real column and card geometry, with the application's existing loading bars. */
export function BoardSkeleton({ focused = false }: { focused?: boolean }) {
 return <>
  <span className="initial-loader-label" role="status">Loading board…</span>
  {Array.from({ length: focused ? 1 : 4 }, (_, column) => <BoardColumn key={column} name="" count="" aria-hidden="true" className="shared-board-skeleton">
   <div className="cards">
    {Array.from({ length: focused ? 24 : 6 }, (_, card) => <div key={card} className="card shared-board-card">
     <div className="head"><span className="initial-skeleton-value" /></div>
     <div className="meta"><span className="initial-skeleton-value" /></div>
    </div>)}
   </div>
  </BoardColumn>)}
 </>
}
