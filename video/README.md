# Videos: demo install, capture, assembly

Short videos of Blerg, one per feature, made from the real UI by scripts, so they can be made again
whenever the product changes. Three steps:

1. **Demo install** (`video/demo/`): a seeded Blerg for filming, separate from any real install on
   the machine. Demo account, board, cards, repositories and sessions; nothing personal on screen.
2. **Capture** (`video/shorts/<short>/capture.mjs`, `video/capture/`): Playwright drives the real
   UI of the demo install and records a take per device (desktop 1920×1080, phone 390 px wide),
   with named marks and honest speed-up markers for waits.
3. **Assembly** (`video/shorts/<short>/spec.yaml`, `video/build/short.py`): the spec cuts scenes
   from the takes and adds captions, voice-over, music, a title card and an end card; out come a
   16:9 and a 9:16 video with burned-in captions.

```bash
video/demo/demo.sh up                              # build what is missing, start, seed
video/capture/run.sh diff-review                   # reset + record the desktop and phone takes
python3 video/build/short.py diff-review --silent  # captions only, no external service
video/demo/demo.sh down                            # remove everything again
```

Outputs go to `video/out/` (ignored by git; set `BLERG_VIDEO_OUT` to put them elsewhere):
`video/out/takes/` for the recordings, `video/out/<slug>/` for the finished videos. Never commit
videos.

Requirements: Docker, Go 1.26+, Node with this repository's `npm ci` done (Playwright and Chromium:
`npx playwright install chromium`), Python 3 with numpy, opencv-python, Pillow and PyYAML, and
ffmpeg built with libass.

## The demo install

`video/demo/demo.sh up | reset | stop | down | status | env`

- **Its own everything.** Ports 28081 (core), 28082 (board), 28083 (runner) and 28432 (Postgres),
  all on loopback; its own databases (the `blerg-demo-postgres` container and `blerg-demo-pgdata`
  volume), its own state in `~/.local/state/blerg-demo`, and its own runner daemon with its own
  `HOME`, repos root and daemon state. It never touches a real install, a real daemon or your
  `~/.claude`. The services get their settings and the demo's throwaway secrets from 0600 env files
  in the state folder, never on a command line. Ports are kept below the kernel's ephemeral port range on purpose: a port inside it
  is now and then held by an outgoing connection, and a server then fails to bind it.
- **Two modes.** `native` (the default) builds core, board and runner from this checkout as host
  binaries and the three web apps with the repository's Vite, and runs them against the Postgres
  container: no image builds, a few hundred MB of state. `BLERG_DEMO_MODE=docker` runs the desktop
  compose stack (`install/desktop/docker-compose.yml`) as the compose project `blerg-demo`, with
  `compose.demo.yml` renaming its images (`blerg-demo-*`) and its sandbox network; the first `up`
  builds three images. In both modes the daemon runs on the host, as in a real desktop install.
- **A scripted agent.** The daemon's `PATH` starts with a `claude` that is
  `video/demo/fake_claude.py`: it speaks the same streaming protocol the daemon drives Claude Code
  with, so the daemon, runner and UI are the real ones and only the model is scripted. What it does
  comes from `video/shorts/*/agent.yaml` (turns matched by the person's message: text, thinking,
  real shell commands such as `blerg-runner diff`, file writes and edits, review replies). No model
  calls, no tokens; usage is reported as zero. Only the seeded sessions' turns
  (`video/demo/seed-agent.yaml`) and the active short's are loaded: `run.sh` sets the short
  (`BLERG_DEMO_SHORT`); with none set, every short's turns load and a message that more than one
  short would answer is logged as a warning. Sessions run on the daemon's host ("This machine"),
  because the scripted engine is not installed in the sandbox image.
- **Seeded state** (`video/demo/seed.mjs`, through the same APIs the browser uses): the account
  `alex` (fixed demo password, in `seed.json`), a board "Acme Photos" with six cards, the demo
  repositories from `video/demo/repos/` (each a fresh git repository tagged `base`), and three
  sessions. The repos root is shown in session headers, so by default it is a private `mktemp`
  folder under `$TMPDIR` (`/tmp/blerg-demo.XXXXXXXX/repos`, mode 0700, recorded in the state), not
  a path under your home folder. This seed is shared by every short; a short that needs other data
  adds it to `seed.mjs` (and its agent's turns for those sessions to `seed-agent.yaml`). `seed.json` in the state folder has the URLs and ids capture scripts use.
- **Reset and tear down.** `reset` (or `up` on a running demo) empties the databases, recreates the
  repositories and the daemon's home, and seeds again; builds are kept, so it takes seconds.
  `down` removes the processes, the containers, the volumes, the `blerg-demo-*` images and the
  state folder. `postgres:16` is shared with the desktop install and kept. In docker mode, Docker's
  build cache keeps the layers until `docker builder prune`.
- **What it deletes: only what it made.** The state folder and the repos root carry a `.blerg-demo`
  marker with the demo's random id bound to that folder's own path and device:inode (and listed in
  the state's record), and each seeded repository's `.git` carries one too, so a copied or moved
  marker never counts. Relative `BLERG_DEMO_STATE`/`BLERG_DEMO_REPOS` are resolved to absolute paths
  first. The script
  refuses to claim or delete a folder that is not empty and has no marker, `$HOME`, `/`, a parent
  of `$HOME`, a folder inside a git repository, or a folder holding a git repository it did not
  create. Under the repos root it removes only the seeded repositories, so pointing
  `BLERG_DEMO_REPOS` at a real folder is refused instead of emptied. Processes are signaled only
  when their environment carries the id, so a pid file left over from before a reboot never
  stops another program; after TERM, any member of a demo-started process group that still carries
  the id is killed, so nothing is orphaned.

## Capture

A capture script records one take per run: `node video/shorts/<short>/capture.mjs desktop|phone`.
`video/capture/run.sh <short> [desktop|phone|all]` resets the demo before each take, so every take
starts from the same state.

`take(short, device, fn)` from `video/capture/lib.mjs` signs in outside the recording, then hands
`fn` a `t` with:

- `t.page`, `t.seed` (the ids from `seed.json`), `t.runnerUrl(path)`, `t.boardUrl(path)`;
- `t.mark(name)`: a named point in the take; the spec cuts scenes between marks;
- `t.wait(label, fn)`: something slow (an agent working). The assembly plays it in at most
  `wait_seconds` and shows **sped up ×N** while it does, so the video never hides how long it took;
- `t.type(locator, text)`: human-paced typing; `t.pause(s)`; `t.shot(name)` for a screenshot.

Every take hides usage meters and notification toasts (`NEUTRAL_CSS`). The desktop take is
Playwright's recording at 1920×1080. The phone take is 390×693 CSS px (9:16) at 3× and is filmed
from the browser's screencast at device pixels (1080×1920), since Playwright's recorder films CSS
pixels.

## Assembly

`python3 video/build/short.py <short> [--format 16x9|9x16] [--silent] [--voice …] [--music …] [--stills T …]`

The spec (`video/shorts/<short>/spec.yaml`):

| Key | What it does |
|---|---|
| `slug` | the output names |
| `formats` | per output (`16x9`, `9x16`), the take it is cut from (`desktop`, `phone`) |
| `title_card`, `end_card` | `title`, `subtitle` / `lines`, `seconds` |
| `scenes` | `from` and `to` (marks), `caption` (one string or a list shown in turn), `vo` (voice-over text), optional `speed`, `pos: top` (or per format, `{16x9: top}`), `min_seconds`, `vo_file`, and `camera` per format: `[[cx, cy, width], [cx, cy, width]]` in take pixels, eased from start to end |
| `wait_seconds` | the longest a recorded wait plays |
| `voice` | `provider`: `elevenlabs` (`voice_id`, `model`), `command` or `files` |
| `music` | `file` (relative to the short's folder or `$BLERG_VIDEO_ASSETS`), `volume` |

A scene lasts as long as its footage, or as its voice-over when that is longer (the last frame
holds). Scenes dissolve into each other.

**Voice-over and music.** No provider, a missing key or `--silent` gives a captions-only video with
a silent audio track: that always works offline. Providers:

- `elevenlabs`: set `ELEVENLABS_API_KEY` (and `voice.voice_id` or `ELEVENLABS_VOICE_ID`) in the
  environment. The key is only sent to ElevenLabs; it is never printed or written to a file.
  Clips are cached by a hash of text, voice and model in `video/out/cache/vo/`.
- `command`: `BLERG_VIDEO_TTS_CMD` is run as `<cmd> <out.wav>` with the line on stdin (any local
  text-to-speech).
- `files`: each scene's `vo_file`.

Music is looped, faded in and out, and ducked under the voice. Keep music files outside git (put
them under `$BLERG_VIDEO_ASSETS`).

Outputs in `video/out/<slug>/`: `<slug>-16x9.mp4`, `<slug>-9x16.mp4`, their `.ass` and `.srt`
captions, and `<slug>-poster.png`.

## Adding a short

1. `video/shorts/<short>/capture.mjs`: copy the diff-review one; drive the feature, `t.mark()` the
   moments, wrap slow parts in `t.wait()`.
2. `video/shorts/<short>/spec.yaml`: scenes between those marks, with captions and voice-over.
3. If an agent has to do something on screen, `video/shorts/<short>/agent.yaml` scripts it (see the
   top of `video/demo/fake_claude.py` for the steps), and the files it writes go beside it. If the
   short needs other seeded data, add it to `video/demo/seed.mjs`.
4. `video/capture/run.sh <short>`, then `python3 video/build/short.py <short>`. Check a few frames
   (`--stills 3 12 20`), and run `scripts/scrub.sh` over everything you commit.

## Tests

`make check-scripts` runs:

- `video/demo/demo_test.sh`: the deletion and process rules above, with no Docker or running demo.
- `video/demo/test_fake_claude.py`: the scripted engine's protocol, turns and short scoping.
- `video/build/test_short.py`: the timeline (waits compressed and labeled, frames held) and the
  ElevenLabs request (voice id escaped, no partial file left).

The Python tests skip themselves when PyYAML (or, for the assembly, numpy, OpenCV or Pillow) is
not installed. The shell scripts were checked with `bash -n`; shellcheck was not available when
they were written, so run it if you have it.

## Rules for what is on screen

- Real UI only; the agent's side may be scripted, and the description says so.
- No hostnames, personal names, real paths or real usage numbers: film only the demo install.
- Slow parts are sped up and say so on screen.
- No videos, music or voice files in git.
