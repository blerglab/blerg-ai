import { useEffect, useState } from "react";
import { api } from "./api";

export interface EngineModel { id: string; name?: string; efforts?: string[]; default_effort?: string }
export interface LaunchEngine { id: string; name: string; models: { models: EngineModel[]; refreshing?: boolean; error?: string } }
export interface LaunchSnapshot { state: "known" | "unknown" | "loading"; engines: LaunchEngine[]; refreshing?: boolean; error?: string }
const ID = /^[a-z][a-z0-9_-]{0,63}$/;
export function parseLaunchOptions(value: unknown): LaunchSnapshot {
  if (!value || typeof value !== "object") throw new Error("Invalid engine discovery");
  const raw = value as Record<string, unknown>;
  if ((raw.state !== "known" && raw.state !== "unknown") || !Array.isArray(raw.engines) || raw.engines.length > 256) throw new Error("Invalid engine discovery");
  const ids = new Set<string>();
  for (const item of raw.engines) {
    if (!item || typeof item !== "object") throw new Error("Invalid engine discovery");
    const e = item as LaunchEngine;
    if (typeof e.id !== "string" || !ID.test(e.id) || ids.has(e.id) || typeof e.name !== "string" || !e.name || e.name.length > 128 || !Array.isArray(e.models?.models) || e.models.models.length > 2048) throw new Error("Invalid engine discovery");
    ids.add(e.id);
    const models = new Set<string>();
    for (const model of e.models.models) {
      if (!model || typeof model.id !== "string" || !model.id || model.id.length > 128 || models.has(model.id) || (model.efforts !== undefined && (!Array.isArray(model.efforts) || !model.efforts.every(x => typeof x === "string" && x.length <= 64)))) throw new Error("Invalid model discovery");
      models.add(model.id);
    }
  }
  return { state: raw.state, engines: raw.state === "known" ? raw.engines as LaunchEngine[] : [], refreshing: raw.refreshing === true || (raw.engines as LaunchEngine[]).some(e => e.models.refreshing), error: typeof raw.error === "string" ? raw.error : undefined };
}

/** Board's backend resolves its stored automation principal. No automation token
 * or catalog from the browser's own account enters this request. */
export function useBoardLaunchOptions(boardId: string | undefined, enabled = true, sessionId?: string) {
  const [revision, setRevision] = useState(0);
  const key = `${boardId ?? ""}\0${sessionId ?? ""}\0${enabled}\0${revision}`;
  const [result, setResult] = useState<LaunchSnapshot & { key: string }>({ key: "", state: "loading", engines: [] });
  useEffect(() => {
    if (!enabled || !boardId) return;
    let live = true;
    let poll: ReturnType<typeof setTimeout> | undefined;
    let refresh = revision > 0;
    const controller = new AbortController();
    const timer = setTimeout(() => {
      if (!live) return;
      live = false;
      controller.abort();
      clearTimeout(poll);
      setResult({ key, state: "unknown", engines: [], error: "Engine discovery timed out. Retry to refresh choices." });
    }, 8_000);
    const read = async () => {
      try {
        const query = refresh ? "?refresh=1" : "";
        refresh = false;
        const snapshot = parseLaunchOptions(await api(`${sessionId ? `/api/runner-sessions/${encodeURIComponent(sessionId)}` : `/api/boards/${encodeURIComponent(boardId)}`}/launch-options${query}`, { signal: controller.signal }));
        if (!live) return;
        setResult({ ...snapshot, key });
        if (snapshot.refreshing) poll = setTimeout(() => { void read(); }, 500);
        else clearTimeout(timer);
      } catch {
        if (!live) return;
        clearTimeout(timer);
        setResult({ key, state: "unknown", engines: [], error: "Engine discovery unavailable. Check the board's automation token and retry." });
      }
    };
    void read();
    return () => { live = false; controller.abort(); clearTimeout(timer); clearTimeout(poll); };
  }, [boardId, sessionId, enabled, key, revision]);
  const current: LaunchSnapshot = !enabled || !boardId ? { state: "unknown", engines: [] } : result.key === key ? result : { state: "loading", engines: [] };
  return { ...current, retry: () => setRevision(x => x + 1) };
}
