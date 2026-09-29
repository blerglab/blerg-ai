import { useState } from "react";
import { effectiveTheme, toggleTheme, type Theme } from "./theme";

// Compact sun/moon switch for the topbar. The icon shows the theme you'd get.
export default function ThemeToggle() {
  const [theme, setThemeState] = useState<Theme>(effectiveTheme);
  const goDark = theme === "light";
  return (
    <button
      type="button"
      className="theme-toggle"
      onClick={() => setThemeState(toggleTheme())}
      title={goDark ? "Switch to dark" : "Switch to light"}
      aria-label={goDark ? "Switch to dark theme" : "Switch to light theme"}
    >
      {goDark ? (
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>
      ) : (
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>
      )}
    </button>
  );
}
