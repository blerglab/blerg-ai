// 404 — the blerg-board has toppled: same five stones as the logo, scattered where
// they fell, blaze mark askew. A lost trail gets signage, not a shrug.
export function NotFound() {
  return (
    <main className="notfound">
      <svg viewBox="0 0 160 96" width="220" role="img" aria-label="A toppled blerg-board — stones scattered on the ground">
        <g stroke="#16181a" strokeWidth="1.5">
          {/* base stone stayed put */}
          <ellipse cx="62" cy="82" rx="24" ry="7.5" fill="#3a4148" />
          {/* the rest went everywhere */}
          <ellipse cx="103" cy="84" rx="17" ry="6" fill="#4a525a" transform="rotate(9 103 84)" />
          <ellipse cx="30" cy="86" rx="13" ry="5.2" fill="#5a636c" transform="rotate(-14 30 86)" />
          <ellipse cx="128" cy="76" rx="10" ry="4.6" fill="#6c757f" transform="rotate(24 128 76)" />
          <ellipse cx="84" cy="70" rx="6.5" ry="3.8" fill="#7f8892" transform="rotate(-38 84 70)" />
        </g>
        {/* the blaze, knocked askew with its stone */}
        <rect x="99" y="81" width="7" height="4.6" rx="0.8" fill="#e8622c" transform="rotate(9 103 84)" />
      </svg>
      <h1 className="sign">Trail lost</h1>
      <p>
        No marker here — the path you followed doesn&apos;t exist, or its stones
        were carried off.
      </p>
      <p>
        <a href="/">← back to the boards</a>
        <span className="sep"> · </span>
        <a href="/onboard">agent onboarding</a>
      </p>
    </main>
  );
}
