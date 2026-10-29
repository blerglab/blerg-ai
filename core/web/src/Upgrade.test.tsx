import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import Upgrade, { upgradeCommands } from "./Upgrade";
import { consumeAccessTokenFromFragment } from "./authClient";
import UpdateNoticeSection from "./UpdateNoticeSection";

afterEach(() => {vi.unstubAllGlobals(); history.replaceState(null,"","/#access_token="); consumeAccessTokenFromFragment();});
it("uses exact commands for the detected install type", () => {
  expect(upgradeCommands("desktop", "0.5.1")[0]).toContain("refs/tags/v0.5.1");
  expect(upgradeCommands("kubernetes", "0.5.1")[0]).toContain("TAG=v0.5.1");
  expect(upgradeCommands("desktop", undefined)).toEqual([]);
  expect(upgradeCommands("unknown", "0.5.1")).toEqual([]);
  expect(upgradeCommands("kubernetes", "$(bad)")).toEqual([]);
});
it("offers generic guidance without disclosing anonymous install type", async () => {
  vi.stubGlobal("fetch",vi.fn().mockResolvedValue({ok:true,json:async()=>({install_type:"unknown",release:"0.5.0"})}));
  render(<Upgrade />);
  expect(await screen.findByText(/Ask an install administrator/)).toBeInTheDocument();
  expect(screen.queryByRole("button",{name:/Copy/})).toBeNull();
  expect(screen.queryByText(/Install type:/)).toBeNull();
});
it("copies the complete desktop procedure for administrators", async () => {
  history.replaceState(null,"","/#access_token=x." + btoa(JSON.stringify({caps:["account.manage"]})) + ".y");
  consumeAccessTokenFromFragment();
  vi.stubGlobal("fetch",vi.fn().mockResolvedValue({ok:true,json:async()=>({install_type:"desktop",release:"0.5.0",latest_version:"0.5.1"})}));
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator,"clipboard",{value:{writeText},configurable:true});
  render(<Upgrade />);
  const buttons = await screen.findAllByRole("button",{name:/^Copy /});
  for (const button of buttons) fireEvent.click(button);
  await waitFor(()=>expect(writeText.mock.calls.map(c=>c[0])).toEqual(upgradeCommands("desktop","0.5.1")));
});
it("saves the admin switch as false",async()=>{
  const fetch = vi.fn().mockResolvedValue({ok:true,json:async()=>({enabled:true})});
  vi.stubGlobal("fetch",fetch);
  render(<UpdateNoticeSection />);
  fireEvent.click(await screen.findByRole("checkbox"));
  fireEvent.click(screen.getByRole("button",{name:"Save release notices"}));
  await screen.findByText("Release notice settings saved.");
  expect(fetch.mock.calls.some(c=>c[1]?.method === "PUT" && c[1]?.body === '{"enabled":false}')).toBe(true);
});
