import { afterEach, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { ReleaseBurn, finishRange } from "./ReleaseBurn";
afterEach(cleanup);
it("shows scope creep, confirmation, daily flow and remaining work in the table", () => {
 const points = [{date:"2026-01-01",scope:2,done:0,confirmed:0,added:2,finished:0},{date:"2026-01-02",scope:3,done:1,confirmed:0,added:1,finished:1}];
 render(<ReleaseBurn burn={{count:points,size:points,estimated:true}} />);
 fireEvent.click(screen.getByText("Data table"));
 expect(screen.getByRole("table",{name:"Release progress data"})).toHaveTextContent("2026-01-02");
 expect(screen.getByText("Confirmed done")).toBeInTheDocument();
 fireEvent.change(screen.getByLabelText("Chart type"),{target:{value:"down"}});
 expect(screen.getByRole("img")).toHaveAccessibleName(/Burn-down/);
 expect(screen.getByRole("columnheader",{name:"Remaining"})).toBeInTheDocument();
 fireEvent.change(screen.getByLabelText("Weight"),{target:{value:"size"}});
 expect(screen.getByText(/S = 1/)).toBeInTheDocument();
});

it("projects a fixed-scope finish scenario from net done progress", () => {
 const p = (date:string,done:number) => ({date,scope:3,done,confirmed:done,added:0,finished:0});
 expect(finishRange([p("2026-01-01",0),p("2026-01-03",2)])).toContain("2026-01-04 to 2026-01-05");
 expect(finishRange([p("2026-01-01",2),p("2026-01-03",1)])).toBe("A finish range needs more completed work.");
});

it("uses count when switching to an unestimated release and labels UTC dates", () => {
 const p={date:"2026-01-01",scope:1,done:0,confirmed:0,added:1,finished:0};
 const {rerender}=render(<ReleaseBurn burn={{count:[p],size:[{...p,scope:8}],estimated:true}} />);
 fireEvent.change(screen.getByLabelText("Weight"),{target:{value:"size"}});
 rerender(<ReleaseBurn burn={{count:[p],size:[p],estimated:false}} />);
 expect(screen.getByLabelText("Weight")).toHaveValue("count");
 expect(screen.queryByText(/S = 1/)).not.toBeInTheDocument();
 expect(screen.getByRole("columnheader",{name:"Date (UTC)"})).toBeInTheDocument();
});

it("discloses post-cut restores and optional chart failures", () => {
 const {rerender}=render(<ReleaseBurn burn={{count:[],size:[],estimated:false,restored:1}} />);
 expect(screen.getByText(/1 card was restored after this release was cut/)).toBeInTheDocument();
 rerender(<ReleaseBurn burn={{count:[],size:[],estimated:false,unavailable:true}} />);
 expect(screen.getByRole("status")).toHaveTextContent("Progress chart is unavailable. Try again later.");
 expect(screen.queryByText("No work in this release yet.")).not.toBeInTheDocument();
});
