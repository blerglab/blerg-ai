import { Fragment, useEffect, useState, type ReactNode } from 'react'

// History, Live and open commit readers keep local snapshots. A scope change
// must discard those mounted readers as well as the shared inventory caches.
export default function DesktopScopeBoundary({children}:{children:ReactNode}) {
  const [scope,setScope]=useState(0)
  useEffect(()=>{
    const reset=()=>setScope(value=>value+1)
    window.addEventListener('blerg-desktop-scope-changed',reset)
    window.addEventListener('blerg-identity-changed',reset)
    return ()=>{
      window.removeEventListener('blerg-desktop-scope-changed',reset)
      window.removeEventListener('blerg-identity-changed',reset)
    }
  },[])
  return <Fragment key={scope}>{children}</Fragment>
}
