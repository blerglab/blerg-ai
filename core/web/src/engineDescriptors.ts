import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";
export type EngineDescriptor = { id: string; name: string; credential_import: boolean; plugins: boolean };
export type EngineMetadata = { engines: EngineDescriptor[]; plugin_engines: string[] };
// Implemented integrations, never the person's login or target readiness.
export async function loadEngineDescriptors(): Promise<EngineMetadata> {
 const controller = new AbortController();
 let timer: ReturnType<typeof setTimeout> | undefined;
 try {
  return await Promise.race([
   (async () => {
    const res = await apiFetch("/api/engines", { signal: controller.signal });
    if (!res.ok) throw new Error("Engine metadata unavailable");
    const data = await res.json() as Partial<EngineMetadata>;
    if (!Array.isArray(data.engines) || data.engines.length > 256 || !Array.isArray(data.plugin_engines) || data.plugin_engines.length > 256 ||
     !data.engines.every(e => e && typeof e.id === "string" && /^[a-z][a-z0-9_-]{0,63}$/.test(e.id) && typeof e.name === "string" && e.name.length <= 128) ||
     !data.plugin_engines.every(id => typeof id === "string" && data.engines!.some(e => e.id === id && e.plugins === true))) throw new Error("Invalid engine metadata");
    return data as EngineMetadata;
   })(),
   new Promise<never>((_, reject) => { timer = setTimeout(() => { controller.abort(); reject(new Error("Engine metadata deadline")); }, 12_000); }),
  ]);
 } finally { if (timer !== undefined) clearTimeout(timer); }
}
export function useEngineDescriptors(enabled = true) {
 const [metadata, setMetadata] = useState<EngineMetadata | null>(null);
 const [error, setError] = useState<string | null>(null);
 const [attempt, setAttempt] = useState(0);
 useEffect(() => {
  if (!enabled) return;
  let live = true;
  void loadEngineDescriptors().then(value => { if (live) { setMetadata(value); setError(null); } }, () => { if (live) setError("Could not load engine integrations. Please try again."); });
  return () => { live = false; };
 }, [enabled, attempt]);
 return { metadata, error, retry: () => { setError(null); setAttempt(n => n + 1); } };
}
