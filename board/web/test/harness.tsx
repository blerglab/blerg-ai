// Page under test for test/run.mjs — a stand-in chat with the same shape as
// BoardChat / Conversation: a fixed-height column, a scrolling `.convo`, and a
// composer below it that grows a line after send (like Composer.grow does).
// The driver pokes it through `window.harness`.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { useStickToBottom } from "../src/useStickToBottom";

export interface Harness {
  append: (n?: number) => void;
  send: () => void;
  mounted: (v: boolean) => void;
  /** swap in a different session's transcript under the same DOM node */
  switchSession: () => void;
  state: () => { top: number; dist: number; mounted: boolean };
}

declare global {
  interface Window { harness: Harness }
}

const transcript = (session: number, n: number) =>
  Array.from({ length: n }, (_, i) => `session ${session} · event ${i}`);

function Chat() {
  const [session, setSession] = useState(1);
  const [events, setEvents] = useState(() => transcript(1, 40));
  const [pending, setPending] = useState<string[]>([]);
  const [shown, setShown] = useState(true);
  const [composer, setComposer] = useState(38);
  const convo = useStickToBottom([events.length, pending.length], { resetKey: session });

  window.harness = {
    append: (n = 1) => setEvents((e) => [...e, ...Array.from({ length: n }, (_, i) => `event ${e.length + i}`)]),
    send: () => {
      setPending((p) => [...p, "sent"]);
      convo.scrollToBottom();
      requestAnimationFrame(() => setComposer(78)); // the composer grows a line
    },
    mounted: setShown,
    switchSession: () => {
      const next = session + 1;
      setSession(next);
      setEvents(transcript(next, 25));
      setPending([]);
    },
    state: () => {
      const el = document.getElementById("convo");
      if (!el) return { top: -1, dist: -1, mounted: false };
      return {
        top: Math.round(el.scrollTop),
        dist: Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
        mounted: true,
      };
    },
  };

  // a genuine unmount: returning a same-type <div> would let React reconcile
  // the node in place, preserving scrollTop and hiding the remount entirely
  if (!shown) return <button>restore</button>;

  return (
    <div style={{ display: "flex", flexDirection: "column", height: 240, width: 320, border: "1px solid #999" }}>
      <div id="convo" ref={convo.ref} onScroll={convo.onScroll}
        style={{ flex: "1 1 auto", minHeight: 0, overflowY: "auto" }}>
        {events.map((t) => <p key={t} style={{ height: 40, margin: 0 }}>{t}</p>)}
        {pending.map((t, i) => <p key={`p${i}`} style={{ height: 40, margin: 0 }}>{t}</p>)}
      </div>
      <div id="composer" style={{ height: composer, borderTop: "1px solid #999" }} />
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<Chat />);
