#!/usr/bin/env bash
# Called by deploy.sh after namespace creation. Never enable shell tracing here.
set -euo pipefail
umask 077
image="${1:?Core image required}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir "$work/authority"
# --ignore-not-found distinguishes first installation from a failed API call.
kubectl get secret blerg-live-data-authority -n blerg --ignore-not-found -o json > "$work/authority.json"
python3 - "$work" <<'PY'
import base64, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
text = (root / 'authority.json').read_text().strip()
if text:
    data = json.loads(text)['data']
    if set(data) != {'identity.json', 'ca.pem', 'key.pem'}:
        raise SystemExit('live_data_authority_invalid')
    for name, value in data.items():
        (root / 'authority' / name).write_bytes(base64.b64decode(value, validate=True))
PY
docker run --rm --network none --read-only --user "$(id -u):$(id -g)" \
  --cap-drop ALL \
  -v "$work:/pki:Z" "$image" live-data-pki --root /pki
# The optional initializer exits successfully even on failure. Never publish a
# partial bundle or replace a known Secret with empty data.
for role in core runner; do
  for name in identity.json key.pem ca.pem cert.pem; do
    test -s "$work/$role/$name" || { echo 'live_data_provisioning_incomplete' >&2; exit 1; }
  done
done
for role in authority core runner; do
  bundle="$work/$role"
  if [ -d "$bundle/bundle" ]; then bundle="$bundle/bundle"; fi
  kubectl create secret generic "blerg-live-data-$role" -n blerg \
    --from-file="$bundle" --dry-run=client -o yaml | kubectl apply --server-side --field-manager=blerg-live-data-pki -f -
done
# Deployments restart when renewed leaf certificates change, including same-tag
# redeploys. Only the public certificates contribute to these annotations.
for role in core runner; do
  sha256sum "$work/$role/cert.pem" | cut -d ' ' -f 1 > "$work/$role.sha"
done
# Caller applies these hashes to its rendered pod templates before rollout.
cp "$work/core.sha" "${2:?hash directory required}/core-live-data.sha"
cp "$work/runner.sha" "$2/runner-live-data.sha"

# The external epoch/key Secret is created once, never overwritten by redeploy.
kubectl get secret blerg-live-data-issuer -n blerg --ignore-not-found -o name > "$work/issuer-name"
if [ ! -s "$work/issuer-name" ]; then
  kubectl create secret generic blerg-live-data-issuer -n blerg --dry-run=client -o yaml | kubectl apply --server-side --field-manager=blerg-live-data-pki -f -
fi
