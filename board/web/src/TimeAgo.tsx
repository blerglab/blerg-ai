import { useEffect, useState } from "react";

// TimeAgo: "32 min ago" with the exact localized time on hover; past a week
// it shows the localized date outright. All times render in the viewer's
// locale — never raw ISO strings.
export function TimeAgo({ iso }: { iso: string }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const iv = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(iv);
  }, []);
  const d = new Date(iso);
  if (isNaN(d.getTime())) return <>{iso}</>;
  const full = d.toLocaleString();
  const secs = (now - d.getTime()) / 1000;
  let label: string;
  if (secs < 60) label = "just now";
  else if (secs < 3600) label = `${Math.round(secs / 60)} min ago`;
  else if (secs < 86400) label = `${Math.round(secs / 3600)} h ago`;
  else if (secs < 7 * 86400) label = `${Math.round(secs / 86400)} d ago`;
  else label = d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
  return <span title={full}>{label}</span>;
}
