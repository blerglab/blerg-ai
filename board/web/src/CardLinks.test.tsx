import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { CardLinks, SafeAnchor, dedupedLinks, safeHref } from "./CardLinks";
import { CardLink } from "./api";

const SESSION = "0b9f3c1e-6a41-4c57-9d0e-2f4a7c8b1e55";
const artifact = (id: string, label: string, session = SESSION): CardLink => ({
  kind: "artifact", url: `https://runner.test/sessions/${session}?artifact=${id}`, label,
});

describe("CardLinks", () => {
  it("shows a runner file as an artifact chip with a link that opens in a new tab", () => {
    render(<CardLinks links={[artifact("f1", "spec.pdf (v2)")]} onOpenDoc={() => {}} />);
    expect(screen.getByText("artifact")).toBeInTheDocument();
    const a = screen.getByRole("link", { name: /spec\.pdf \(v2\)/ });
    expect(a).toHaveAttribute("href", `https://runner.test/sessions/${SESSION}?artifact=f1`);
    expect(a).toHaveAttribute("target", "_blank");
    expect(a.getAttribute("rel")).toContain("noopener");
  });

  it("keeps two files with the same label from different sessions", () => {
    const other = "11111111-2222-4333-8444-555555555555";
    const out = dedupedLinks([artifact("f1", "report.pdf (v1)"), artifact("f1", "report.pdf (v1)", other)]);
    expect(out).toHaveLength(2);
  });

  it("still collapses repeated links of the other kinds by label", () => {
    const out = dedupedLinks([
      { kind: "session", url: "https://r/s/1", label: "runner session" },
      { kind: "session", url: "https://r/s/2", label: "runner session" },
    ]);
    expect(out.map((l) => l.url)).toEqual(["https://r/s/2"]);
  });

  it("never makes a script or data address a link", () => {
    expect(safeHref("javascript:alert(1)")).toBeNull();
    expect(safeHref("data:text/html,<b>x</b>")).toBeNull();
    expect(safeHref("not a url")).toBeNull();
    expect(safeHref("https://example.com/a")).toBe("https://example.com/a");
    render(<CardLinks links={[{ kind: "url", url: "javascript:alert(1)", label: "click me" }]} onOpenDoc={() => {}} />);
    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getByText("click me")).toBeInTheDocument();
  });

  it("opens a doc link in the viewer, not as an anchor", () => {
    const onOpen = vi.fn();
    render(<CardLinks links={[{ kind: "doc", url: "https://x/doc.md", label: "the doc" }]} onOpenDoc={onOpen} />);
    fireEvent.click(screen.getByText("the doc"));
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  it("renders nothing without links", () => {
    const { container } = render(<CardLinks links={null} onOpenDoc={() => {}} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("SafeAnchor makes only http(s) addresses clickable, whatever the kind of link around it", () => {
    const { container, rerender } = render(<SafeAnchor href="javascript:alert(1)">pr title</SafeAnchor>);
    expect(container.querySelector("a")).toBeNull();
    expect(screen.getByText("pr title")).toBeInTheDocument();
    rerender(<SafeAnchor href="https://github.com/o/r/pull/3">pr title</SafeAnchor>);
    const a = container.querySelector("a")!;
    expect(a.getAttribute("href")).toBe("https://github.com/o/r/pull/3");
    expect(a.getAttribute("rel")).toContain("noopener");
    rerender(<SafeAnchor href={undefined}>nothing</SafeAnchor>);
    expect(container.querySelector("a")).toBeNull();
  });

  it("hides the decorative arrow from assistive technology", () => {
    const { container } = render(<CardLinks links={[artifact("f1", "spec.pdf (v1)")]} onOpenDoc={() => {}} />);
    expect(container.querySelector('[aria-hidden="true"]')?.textContent).toContain("↗");
  });
});
