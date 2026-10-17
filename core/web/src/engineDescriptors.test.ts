import { afterEach, describe, expect, it, vi } from "vitest";
import { loadEngineDescriptors } from "./engineDescriptors";
vi.mock("./apiFetch",()=>({apiFetch:vi.fn()}));
import { apiFetch } from "./apiFetch";
afterEach(()=>{vi.useRealTimers();vi.clearAllMocks();});
describe("engine descriptor discovery",()=>{
 it("uses registered metadata without inventing engine availability",async()=>{
  vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({engines:[{id:"fixture",name:"Fixture",credential_import:true,plugins:true}],plugin_engines:["fixture"]})));
  expect(await loadEngineDescriptors()).toEqual({engines:[{id:"fixture",name:"Fixture",credential_import:true,plugins:true}],plugin_engines:["fixture"]});
 });
 it("bounds even an unresolved authentication refresh",async()=>{
  vi.useFakeTimers();vi.mocked(apiFetch).mockImplementation(()=>new Promise(()=>{}));
  const result=loadEngineDescriptors().catch(()=>"timeout");
  await vi.advanceTimersByTimeAsync(12_000);expect(await result).toBe("timeout");
 });
 it("rejects malformed metadata instead of restoring fixed engines",async()=>{
  vi.mocked(apiFetch).mockResolvedValue(new Response("{}"));
  await expect(loadEngineDescriptors()).rejects.toThrow();
 });
});
