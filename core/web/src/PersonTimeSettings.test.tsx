import { afterEach, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import PersonTimeSettings from "./PersonTimeSettings";
import { apiFetch } from "./apiFetch";
vi.mock("./apiFetch",()=>({apiFetch:vi.fn()}));
afterEach(()=>vi.resetAllMocks());
function reply(value:unknown,ok=true){return {ok,json:async()=>value} as Response;}
it("initializes an unset account with the browser zone and uses the server's winning value",async()=>{
 vi.mocked(apiFetch).mockResolvedValueOnce(reply({timezone:""})).mockResolvedValueOnce(reply({timezone:"America/New_York"}));
 render(<PersonTimeSettings/>);
 await waitFor(()=>expect(screen.getByLabelText("Time zone")).toHaveValue("America/New_York"));
 expect(apiFetch).toHaveBeenNthCalledWith(2,"/api/me/time",expect.objectContaining({body:JSON.stringify({timezone:Intl.DateTimeFormat().resolvedOptions().timeZone,initialize:true})}));
});
it("preserves a saved zone and saves edits without overwriting it with the browser zone",async()=>{
 vi.mocked(apiFetch).mockResolvedValueOnce(reply({timezone:"America/New_York"})).mockResolvedValueOnce(reply({timezone:"Europe/Paris"}));
 render(<PersonTimeSettings/>);
 const input=await screen.findByLabelText("Time zone");
 await waitFor(()=>expect(input).toHaveValue("America/New_York"));
 fireEvent.change(input,{target:{value:"Europe/Paris"}});
 fireEvent.click(screen.getByRole("button",{name:"Save time settings"}));
 expect(await screen.findByRole("status")).toHaveTextContent("Time settings saved.");
 expect(apiFetch).toHaveBeenLastCalledWith("/api/me/time",expect.objectContaining({body:JSON.stringify({timezone:"Europe/Paris",quiet_hours:{enabled:false,start:"22:00",end:"08:00"}})}));
});
it("shows loading and save failures without claiming success",async()=>{
 vi.mocked(apiFetch).mockResolvedValueOnce(reply({timezone:"UTC"})).mockResolvedValueOnce(reply({},false));
 render(<PersonTimeSettings/>);
 await waitFor(()=>expect(screen.getByLabelText("Time zone")).toHaveValue("UTC"));
 fireEvent.change(screen.getByLabelText("Time zone"),{target:{value:"Europe/Paris"}});
 fireEvent.click(screen.getByRole("button",{name:"Save time settings"}));
 expect(await screen.findByRole("alert")).toHaveTextContent("Could not save");
 expect(screen.queryByRole("status")).toBeNull();
});

it("quiet hours default off and save a local daily window",async()=>{
 vi.mocked(apiFetch).mockResolvedValueOnce(reply({timezone:"America/New_York"})).mockResolvedValueOnce(reply({timezone:"America/New_York",quiet_hours:{enabled:true,start:"23:00",end:"07:00"}}));
 render(<PersonTimeSettings/>);
 await waitFor(()=>expect(screen.getByLabelText("Time zone")).toHaveValue("America/New_York"));
 const toggle=screen.getByRole("checkbox",{name:"Quiet hours"});expect(toggle).not.toBeChecked();
 fireEvent.click(toggle);fireEvent.change(screen.getByLabelText("From"),{target:{value:"23:00"}});fireEvent.change(screen.getByLabelText("Until"),{target:{value:"07:00"}});
 fireEvent.click(screen.getByRole("button",{name:"Save time settings"}));
 expect(await screen.findByRole("status")).toHaveTextContent("Time settings saved.");
 expect(apiFetch).toHaveBeenLastCalledWith("/api/me/time",expect.objectContaining({body:JSON.stringify({timezone:"America/New_York",quiet_hours:{enabled:true,start:"23:00",end:"07:00"}})}));
});

it("offers searchable IANA locations and refuses fixed-offset abbreviations",async()=>{
 vi.mocked(apiFetch).mockResolvedValueOnce(reply({timezone:"UTC"}));
 render(<PersonTimeSettings/>);
 await waitFor(()=>expect(screen.getByLabelText("Time zone")).toHaveValue("UTC"));
 const field=screen.getByLabelText("Time zone");
 expect(field).toHaveAttribute("list","person-timezones");
 expect(document.querySelector('datalist option[value="America/New_York"]')).toBeTruthy();
 fireEvent.change(field,{target:{value:"EST"}});
 expect(screen.getByRole("button",{name:"Save time settings"})).toBeDisabled();
 expect(apiFetch).toHaveBeenCalledTimes(1);
});
