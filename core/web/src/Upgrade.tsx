import { useEffect, useState } from "react";
import Shell, { useSite } from "./Shell";
import { apiFetch } from "./apiFetch";
import { getAccessToken } from "./authClient";

export function upgradeCommands(type: string | undefined, latest: string | undefined, releaseURL?: string): string[] {
  if (!latest || !/^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$/.test(latest) || !["desktop", "kubernetes"].includes(type || "")) return [];
  const prefix = "https://github.com/blerglab/blerg-ai/releases/tag/";
  if (releaseURL && releaseURL !== prefix + latest && releaseURL !== prefix + "v" + latest) return [];
  const tag = releaseURL === prefix + latest ? latest : `v${latest}`;
  // One copyable procedure: no later deployment command can outlive a failed checkout.
  return [`bash <<'BLERG_UPGRADE'
set -euo pipefail
source_root="$(git rev-parse --show-toplevel)"
cd "$source_root"
[ -z "$(git status --porcelain)" ] || { echo 'Commit or stash local changes before upgrading.' >&2; exit 1; }
[ -f install/${type === "desktop" ? "desktop" : "k8s"}/.env ] || { echo 'Install configuration is missing.' >&2; exit 1; }
release_dir="$(mktemp -d "$source_root/../blerg-release-${tag}.XXXXXX")"
chmod 700 "$release_dir"
git -C "$release_dir" init -q
git -C "$release_dir" fetch --depth=1 https://github.com/blerglab/blerg-ai.git refs/tags/${tag}
revision="$(git -C "$release_dir" rev-parse 'FETCH_HEAD^{commit}')"
git -C "$release_dir" checkout --detach "$revision"
cd "$release_dir"
[ "$(git rev-parse HEAD)" = "$revision" ]
[ "$(cat VERSION)" = '${latest}' ]
[ -z "$(git status --porcelain)" ]
cp "$source_root/install/${type === "desktop" ? "desktop" : "k8s"}/.env" install/${type === "desktop" ? "desktop" : "k8s"}/.env
chmod 600 install/${type === "desktop" ? "desktop" : "k8s"}/.env
${type === "desktop" ? '(cd install/desktop && ./blerg-up.sh && ./daemon/install.sh install)' : `sed -i 's|^TAG=.*|TAG=${tag}|' install/k8s/.env\n(cd install/k8s && ./deploy.sh)`}
echo "Upgraded from $release_dir. Keep this directory for the installed source and configuration."
BLERG_UPGRADE`];
}

export default function Upgrade() {
  const site = useSite();
  const [installType, setInstallType] = useState<string>();
  const [copied, setCopied] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    if (!getAccessToken()) return;
    let cancelled = false;
    apiFetch("/api/settings/update-notice").then(r => r.ok ? r.json() : null)
      .then(value => { if (!cancelled) setInstallType(value?.install_type); }).catch(() => {});
    return () => { cancelled = true; };
  }, []);
  const commands = upgradeCommands(installType, site?.latest_version, site?.latest_url);
  return <Shell site={site}><section className="section">
    <h1>How to upgrade</h1>
    {installType && <p>Install type: {installType}.</p>}
    {commands.length > 0 ? <>
      <p>Run this complete procedure from a clean repository root on the install host. It fetches the advertised release from Blerg’s public repository, verifies the checkout, copies your install configuration, and stops on failure. Keep the new release directory afterward. Review the release notes before upgrading.</p>
      {commands.map(command => <div className="card" key={command} style={{ display: "flex", gap: 16, flexWrap: "wrap", marginBottom: 12 }}>
        <pre style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere", minWidth: 0, flex: "1 1 220px" }}><code>{command}</code></pre>
        <button className="btn" onClick={async () => {
          try { await navigator.clipboard.writeText(command); setCopied(command); setError(""); }
          catch { setError("Could not copy. Select and copy the command above."); }
        }} aria-label={`Copy ${command}`}>{copied === command ? "Copied" : "Copy"}</button>
      </div>)}
    </> : <p>Ask an install administrator to sign in for install-specific upgrade instructions. Review the release notes and follow the installation guide for your deployment. If no newer stable release is available, no upgrade command is offered.</p>}
    {error && <p role="alert">{error}</p>}
  </section></Shell>;
}
