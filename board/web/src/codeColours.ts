// The person's code colours in the board's chat: the same choice they made in Blerg Runner
// (Settings → Code colours…), read through the chat proxy (GET /api/chat/preferences) and painted on
// the page. The board has no editor for it. The last choice is cached in localStorage so the first
// paint uses it; the server's value wins when it arrives.
import { useEffect } from "react";
import { DEFAULT_CODE_COLOURS, applyCodeColours, currentCodeMode, normaliseCodeColours, type CodeColours } from "@blerglab/chat";
import { api } from "./api";

export const CODE_COLOURS_KEY = "blerg-board.code-colours";

function readStored(): CodeColours {
  try {
    const raw = localStorage.getItem(CODE_COLOURS_KEY);
    return raw ? normaliseCodeColours(JSON.parse(raw)) : DEFAULT_CODE_COLOURS;
  } catch {
    return DEFAULT_CODE_COLOURS;
  }
}

let current: CodeColours = readStored();

/** Paint the current choice on the page, in the mode the page shows now. */
export function applyCodeColoursToPage(): void {
  applyCodeColours(document.documentElement, current, currentCodeMode());
}

function set(c: CodeColours): void {
  current = c;
  try { localStorage.setItem(CODE_COLOURS_KEY, JSON.stringify(c)); } catch { /* private mode: this page only */ }
  applyCodeColoursToPage();
}

/** Read the person's choice from the runner (through the board) and take it; a failure keeps what is cached. */
export async function loadCodeColours(): Promise<void> {
  try {
    const body = await api<{ code_colours?: unknown }>("/api/chat/preferences");
    // Nothing set on the runner means the defaults, even if an older choice is cached here.
    const next = body?.code_colours ? normaliseCodeColours(body.code_colours) : DEFAULT_CODE_COLOURS;
    if (JSON.stringify(next) !== JSON.stringify(current)) set(next);
    else applyCodeColoursToPage();
  } catch {
    applyCodeColoursToPage();
  }
}

/** Mounted once by the shell: paints the cached choice, repaints when the page's mode changes, and loads the server's. */
export function useCodeColours(): void {
  useEffect(() => {
    applyCodeColoursToPage();
    const obs = new MutationObserver(applyCodeColoursToPage);
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    const mq = window.matchMedia?.("(prefers-color-scheme: light)");
    mq?.addEventListener?.("change", applyCodeColoursToPage);
    void loadCodeColours();
    return () => {
      obs.disconnect();
      mq?.removeEventListener?.("change", applyCodeColoursToPage);
    };
  }, []);
}
