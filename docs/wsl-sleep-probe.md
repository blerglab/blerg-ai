# WSL host sleep clock probe (BLG-400)

This diagnostic collects evidence for Blerg 0.6.0. It does not change the WSL
persistence refusal from BLG-269. Run it **inside the WSL distribution** on the
Windows laptop that will sleep, rather than in a container or remote session.

For the short laptop checklist and exact files to send back, see the
[probe README](../runner/cmd/wsl-sleep-probe/README.md).

From the repository's `runner` directory:

```console
GOWORK=off go build -o /tmp/wsl-sleep-probe ./cmd/wsl-sleep-probe
/tmp/wsl-sleep-probe record -out /tmp/sleep-01.jsonl
```

Leave it running. First collect about 30 seconds awake, then use Windows Sleep.
After waking, leave it awake for another 30 seconds. Repeat several real sleeps,
including a longer sleep. Note each approximate Windows sleep and wake time and
how sleep was triggered, ideally using an independent clock. Press Ctrl-C after
the last awake interval. Do not shut down WSL, reboot, or restart the probe during
a recording. Use a new filename for a new run; existing files are never replaced.

The command samples immediately, then waits one second after each saved sample.
It records UTC wall time, raw CLOCK_BOOTTIME and raw CLOCK_MONOTONIC nanoseconds
in versioned JSONL, with a sequence number. Each sample is written and synced to
a local file (mode 0600). Sequential clock reads and file I/O add a small amount
of skew and scheduling delay. There is no catch-up sampling after resume.
It records no hostname or credentials.

Read a recording (the reader also builds on non-Linux systems):

```console
/tmp/wsl-sleep-probe read /tmp/sleep-01.jsonl > /tmp/sleep-01-gaps.tsv
```

Each row after the header describes one adjacent pair: ending sample and UTC
time, wall gap, BOOTTIME gap, MONOTONIC gap, BOOTTIME minus MONOTONIC, and wall
minus BOOTTIME. Durations use Go notation (`1m1s`, `-2s`, etc.). No threshold hides
small gaps. Fewer than two samples are rejected because they contain no gap. Invalid or truncated input exits
nonzero; any rows already printed are partial results, not a successful report.
Do not concatenate runs. Keep the raw file if a crash leaves a partial last line;
any recovery should use a separate copy.

Compare rows with the independently noted real sleep intervals. If BOOTTIME
includes a sleep that MONOTONIC excludes, their difference should approximate
that sleep. If both guest clocks stop, wall minus BOOTTIME may show the missing
time. If all clocks advance together, these differences cannot distinguish host
sleep from delayed scheduling. Wall adjustments, clock resynchronization and VM
pauses can also affect these numbers; a gap alone does not prove sleep.

Keep the raw JSONL, TSV, sleep notes, Windows/WSL versions and WSL kernel release
(`uname -r`) together privately. Note time synchronization changes and whether
Windows used sleep, hibernation or Modern Standby. Do not commit real captures or
machine identifiers. Share the reviewed evidence with the owner for BLG-400 and
related BLG-269/BLG-271. The persistence refusal remains until that evidence is
reviewed and a separate kernel/signal decision and regression test are made.
