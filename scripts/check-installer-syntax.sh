#!/usr/bin/env bash
# Run inside bash:3.2; parse every installer script without executing it.
set -euo pipefail
cd "$(dirname "$0")/.."
case "$BASH_VERSION" in 3.2.*) ;; *) echo 'Bash 3.2 is required' >&2; exit 1 ;; esac
bash --version | head -1
failed=0
count=0
while IFS= read -r -d '' file; do
  bash -n "$file" || failed=1
  count=$((count + 1))
done < <(find install -type f -name '*.sh' -print0)
echo "Checked $count installer shell scripts"
exit "$failed"
