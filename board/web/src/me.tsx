// me.tsx — who the current viewer is, for member-aware UI gating.
//
// Hiding a control here is a UX nicety only: the server enforces the same
// capability (board.admin, via auth.Principal.RequireAdmin/RequireBoard —
// see board/internal/auth/auth.go) on every admin-only route regardless of
// what the client renders, so a member who forges the request (or a stale
// tab) still gets a 403. is_admin below mirrors that same capability check
// (auth.Principal.IsAdmin, which handleMe calls directly) — it is not a
// guess derived from token kind.
import { createContext, ReactNode, useContext, useEffect, useState } from "react";
import { api } from "./api";

export interface Me {
  kind: string;
  is_admin: boolean;
  is_human: boolean;
  // A signed-in person's own blerg-core account id (absent for tokens).
  account_id?: string;
  capabilities?: string[];
}

const MeContext = createContext<Me | null>(null);

export function MeProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null);
  useEffect(() => {
    api<Me>("/api/me").then(setMe).catch(() => setMe(null));
  }, []);
  return <MeContext.Provider value={me}>{children}</MeContext.Provider>;
}

export function useMe(): Me | null {
  return useContext(MeContext);
}
