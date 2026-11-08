# WSL sleep evidence (BLG-400)

Run in a terminal **inside the laptop's WSL distribution**, outside containers.
Use Go 1.26.9 or newer. From the repository root:

```sh
cd runner
GOWORK=off go build -o /tmp/wsl-sleep-probe ./cmd/wsl-sleep-probe
mkdir -p "$HOME/wsl-sleep-evidence"
capture="$HOME/wsl-sleep-evidence/sleep-$(date -u +%Y%m%dT%H%M%SZ).jsonl"
/tmp/wsl-sleep-probe record -out "$capture"
```

Keep this terminal and process running for at least 15 minutes. Collect 30 seconds
awake, then use Windows **Sleep** and wake the laptop. Repeat **three times**,
including one sleep of at least five minutes; leave 30 seconds awake between
sleeps and after the last wake. Note sleep/wake times using an independent clock,
how Sleep was triggered, and whether it was Modern Standby or hibernation.
Do not reboot or shut down WSL. Press Ctrl-C after the last awake interval.

In that same terminal:

```sh
/tmp/wsl-sleep-probe read "$capture" > "$capture.gaps.tsv"
printf '%s\n' "$capture" "$capture.gaps.tsv"
uname -r > "$capture.kernel.txt"
```

Files land in **~/wsl-sleep-evidence/** in the WSL filesystem. Send the raw
`.jsonl`, `.jsonl.gaps.tsv`, `.jsonl.kernel.txt`, sleep/wake notes, and Windows
and WSL versions privately to the BLG-400 owner. Keep original captures; do not
commit them. Each recording requires a new filename.

The recorder syncs UTC wall time, CLOCK_BOOTTIME, and CLOCK_MONOTONIC to disk
roughly every second. The reader reports every adjacent gap, including ordinary
awake intervals. Its differences help compare clocks with your sleep notes;
they cannot independently prove host sleep or distinguish wall adjustments.
A failed read may leave partial output; do not treat that TSV as complete.
At least two complete samples are required.

The WSL persistence refusal remains unchanged. Details:
[clock interpretation](../../../docs/wsl-sleep-probe.md).
The test fixtures are synthetic recorded-format data, not laptop evidence.
