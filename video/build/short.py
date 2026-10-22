#!/usr/bin/env python3
"""Assemble a short from its spec and its recorded takes.

    video/build/short.py <short> [--format 16x9|9x16] [--silent] [--stills T ...]

Reads video/shorts/<short>/spec.yaml and the takes video/capture/run.sh recorded
(video/out/takes/<slug>-<take>.webm + .json), and writes video/out/<slug>/:

    <slug>-16x9.mp4, <slug>-9x16.mp4   H.264 + AAC, captions burned in
    <slug>-<fmt>.ass, <slug>-<fmt>.srt the captions (the .srt for platforms that take them)
    <slug>-poster.png                  a frame of the last scene, no caption

This generalizes the first marketing video's pipeline (build_v2.py: ffmpeg decodes, Python composes
frames with a sub-pixel camera and an honest "sped up xN" badge, libass burns the captions, the audio
is muxed): the scene list, captions, voice-over, music and cards now come from a spec file, and the
timeline from the marks a capture script records.

Timeline. A scene plays its take from mark `from` to mark `to`. Any recorded wait inside it (a
capture's t.wait(...)) is played in at most `wait_seconds` and badged "sped up xN" while it is; the
rest plays in real time (or at the scene's `speed`). When the scene's voice-over is longer than its
footage, the last frame holds. Scenes dissolve into each other; a title card opens and an end card
closes the short.

Audio. Voice-over per scene (spec `voice`): elevenlabs (ELEVENLABS_API_KEY, never printed or
written anywhere; clips cached by a hash of text and voice under video/out/cache/vo/), command
(BLERG_VIDEO_TTS_CMD, run as `<cmd> <out.wav>` with the text on stdin), or files (scene `vo_file`).
Without a provider, a key or with --silent the short is captions only, with a silent audio track.
Music (spec `music.file`) is looped, faded and ducked under the voice.

Needs python3 with numpy, opencv (cv2), Pillow and PyYAML, and ffmpeg built with libass.
"""
import argparse
import hashlib
import json
import math
import os
import shlex
import subprocess
import sys
import urllib.parse
import urllib.request

import cv2
import numpy as np
import yaml
from PIL import Image, ImageDraw, ImageFilter, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
VIDEO = os.path.dirname(HERE)
OUT = os.environ.get("BLERG_VIDEO_OUT") or os.path.join(VIDEO, "out")
FPS = 30
XF = 0.35  # dissolve between scenes (s)
BG = (13, 17, 23)
ORANGE = (255, 106, 61)

FORMATS = {
    "16x9": dict(W=1920, H=1080, cap_size=50, cap_margin=56, cap_maxw=1400, badge=22, title=96, line=40),
    "9x16": dict(W=1080, H=1920, cap_size=54, cap_margin=300, cap_maxw=900, badge=30, title=104, line=44),
}

# ---------------------------------------------------------------- fonts
FONT_DIRS = [d for d in (os.environ.get("BLERG_VIDEO_FONTS"), "/usr/share/fonts/truetype/dejavu") if d]
FONT_FILES = {"bold": ["seguisb.ttf", "segoeuib.ttf", "DejaVuSans-Bold.ttf"], "regular": ["segoeui.ttf", "DejaVuSans.ttf"]}


def font(kind, size):
    for d in FONT_DIRS:
        for name in FONT_FILES[kind]:
            p = os.path.join(d, name)
            if os.path.exists(p):
                return ImageFont.truetype(p, size)
    return ImageFont.load_default()


def ass_font():
    d = os.environ.get("BLERG_VIDEO_FONTS")
    if d and os.path.exists(os.path.join(d, "seguisb.ttf")):
        return "Segoe UI Semibold", d
    return "DejaVu Sans", "/usr/share/fonts/truetype/dejavu"


# ---------------------------------------------------------------- helpers
def ease(t):
    t = min(max(t, 0.0), 1.0)
    return 0.5 - 0.5 * math.cos(math.pi * t)


def run(cmd, **kw):
    return subprocess.run(cmd, check=True, **kw)


def probe_duration(path):
    out = subprocess.run(["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path],
                         capture_output=True, text=True, check=True).stdout.strip()
    return float(out)


def wrap(text, fnt, maxw):
    words, lines, cur = text.split(), [], ""
    for w in words:
        t = (cur + " " + w).strip()
        if fnt.getlength(t) <= maxw or not cur:
            cur = t
        else:
            lines.append(cur)
            cur = w
    if cur:
        lines.append(cur)
    return lines


def ass_time(t):
    t = max(0.0, t)
    h, rem = divmod(t, 3600)
    m, s = divmod(rem, 60)
    return f"{int(h)}:{int(m):02d}:{s:05.2f}"


def srt_time(t):
    ms = int(round(max(0.0, t) * 1000))
    h, ms = divmod(ms, 3600000)
    m, ms = divmod(ms, 60000)
    s, ms = divmod(ms, 1000)
    return f"{h:02d}:{m:02d}:{s:02d},{ms:03d}"


# ---------------------------------------------------------------- spec + takes
class Spec:
    def __init__(self, short):
        self.dir = os.path.join(VIDEO, "shorts", short)
        with open(os.path.join(self.dir, "spec.yaml"), encoding="utf-8") as f:
            self.d = yaml.safe_load(f)
        self.slug = self.d.get("slug", short)
        self.scenes = self.d["scenes"]

    def asset(self, rel):
        if not rel:
            return None
        for base in (self.dir, os.environ.get("BLERG_VIDEO_ASSETS") or ""):
            p = os.path.join(base, rel) if base else rel
            if os.path.exists(p):
                return p
        return None


def load_take(spec, name):
    meta_path = os.path.join(OUT, "takes", f"{spec.slug}-{name}.json")
    if not os.path.exists(meta_path):
        sys.exit(f"no take {meta_path}: record it with video/capture/run.sh {spec.slug}")
    with open(meta_path, encoding="utf-8") as f:
        meta = json.load(f)
    meta["path"] = os.path.join(OUT, "takes", meta["video"])
    meta["marks"]["start"] = 0.0
    meta["marks"].setdefault("end", meta["duration"])
    return meta


def pieces(spec, scene, take):
    """[(src_a, src_b, speed, label)] covering the scene's source range: waits compressed."""
    m = take["marks"]
    a, b = m[scene["from"]], m[scene["to"]]
    base = float(scene.get("speed", 1.0))
    target = float(spec.d.get("wait_seconds", 3.0))
    cuts = []
    for w in take.get("waits", []):
        wa, wb = max(a, w["start"]), min(b, w["end"])
        if wb - wa > 0.2:
            cuts.append((wa, wb))
    out, t = [], a
    for wa, wb in sorted(cuts):
        if wa > t:
            out.append((t, wa, base, None))
        sp = max(base, (wb - wa) / target)
        out.append((wa, wb, sp, "wait"))
        t = wb
    if b > t:
        out.append((t, b, base, None))
    return out


# ---------------------------------------------------------------- voice-over
def tts(spec, text, idx, silent):
    if silent or not text:
        return None
    v = spec.d.get("voice") or {}
    provider = v.get("provider")
    if provider == "elevenlabs" and not os.environ.get("ELEVENLABS_API_KEY"):
        if not getattr(tts, "warned", False):
            print("  voice: ELEVENLABS_API_KEY is not set, building captions only", file=sys.stderr)
            tts.warned = True
        return None
    cache = os.path.join(OUT, "cache", "vo")
    os.makedirs(cache, exist_ok=True)
    if provider == "files":
        return spec.asset(spec.scenes[idx].get("vo_file"))
    if provider == "elevenlabs":
        key = os.environ.get("ELEVENLABS_API_KEY")
        voice = v.get("voice_id") or os.environ.get("ELEVENLABS_VOICE_ID")
        if not key or not voice:
            return None
        model = v.get("model", "eleven_multilingual_v2")
        h = hashlib.sha256(f"elevenlabs|{voice}|{model}|{text}".encode()).hexdigest()[:20]
        path = os.path.join(cache, f"{h}.mp3")
        if not os.path.exists(path):
            req = urllib.request.Request(
                "https://api.elevenlabs.io/v1/text-to-speech/" + urllib.parse.quote(voice, safe=""),
                data=json.dumps({"text": text, "model_id": model}).encode(),
                headers={"xi-api-key": key, "Content-Type": "application/json", "Accept": "audio/mpeg"})
            try:
                with urllib.request.urlopen(req, timeout=120) as r, open(path + ".tmp", "wb") as f:
                    f.write(r.read())
                os.replace(path + ".tmp", path)
            finally:
                if os.path.exists(path + ".tmp"):
                    os.remove(path + ".tmp")
        return path
    if provider == "command":
        cmd = os.environ.get("BLERG_VIDEO_TTS_CMD")
        if not cmd:
            return None
        h = hashlib.sha256(f"cmd|{cmd}|{text}".encode()).hexdigest()[:20]
        path = os.path.join(cache, f"{h}.wav")
        if not os.path.exists(path):
            run(shlex.split(cmd) + [path], input=text.encode())
        return path
    return None


# ---------------------------------------------------------------- timeline
def timeline(spec, fmt, take, silent):
    """Ordered items with output times: cards and scenes, plus the voice-over clips."""
    items, t = [], 0.0
    tc = spec.d.get("title_card")
    if tc:
        d = float(tc.get("seconds", 2.5))
        items.append({"kind": "title", "t0": t, "dur": d})
        t += d
    vo = []
    for i, sc in enumerate(spec.scenes):
        ps = pieces(spec, sc, take)
        foot = sum((b - a) / s for a, b, s, _ in ps)
        clip = tts(spec, sc.get("vo", ""), i, silent)
        vdur = probe_duration(clip) if clip else 0.0
        dur = max(foot, vdur + 0.5, float(sc.get("min_seconds", 1.6)))
        items.append({"kind": "scene", "i": i, "t0": t, "dur": dur, "pieces": ps, "foot": foot})
        if clip:
            vo.append((t + 0.2, clip))
        t += dur
    ec = spec.d.get("end_card")
    if ec:
        d = float(ec.get("seconds", 3.0))
        items.append({"kind": "end", "t0": t, "dur": d})
        t += d
    return items, t, vo


def src_time(item, local):
    """Source time and speed for a scene at local output time; past the footage, the last frame."""
    acc = 0.0
    for a, b, s, label in item["pieces"]:
        seg = (b - a) / s
        if local < acc + seg:
            return a + (local - acc) * s, s, label
        acc += seg
    a, b, s, label = item["pieces"][-1]
    return b - 0.05, 1.0, None


# ---------------------------------------------------------------- frames
class Reader:
    """Sequential frames of a take, decoded at FPS; random forward access by time."""

    def __init__(self, path, W, H):
        self.W, self.H = W, H
        self.proc = subprocess.Popen(["ffmpeg", "-v", "error", "-i", path, "-an", "-vf", f"fps={FPS}",
                                      "-f", "rawvideo", "-pix_fmt", "rgb24", "-"], stdout=subprocess.PIPE)
        self.n = -1
        self.cur = None

    def at(self, t):
        want = max(0, int(t * FPS))
        while self.n < want:
            buf = self.proc.stdout.read(self.W * self.H * 3)
            if len(buf) < self.W * self.H * 3:
                break
            self.cur = np.frombuffer(buf, np.uint8).reshape(self.H, self.W, 3)
            self.n += 1
        return self.cur

    def close(self):
        self.proc.kill()
        self.proc.wait()


def fit(frame, W, H, cam=None):
    """The frame in the output size: a camera view (cx, cy, w in source px) or a centered cover."""
    sh, sw = frame.shape[:2]
    if cam is None:
        if (sw, sh) == (W, H):
            return frame
        s = max(W / sw, H / sh)
        w = W / s
        cx, cy = sw / 2, sh / 2
    else:
        cx, cy, w = cam
    h = w * H / W
    x0 = min(max(cx - w / 2, 0), max(0, sw - w))
    y0 = min(max(cy - h / 2, 0), max(0, sh - h))
    s = W / w
    if s < 1:
        small = cv2.resize(frame, (int(round(sw * s)), int(round(sh * s))), interpolation=cv2.INTER_AREA)
        M = np.float32([[1, 0, -x0 * s], [0, 1, -y0 * s]])
        return cv2.warpAffine(small, M, (W, H), flags=cv2.INTER_LINEAR, borderMode=cv2.BORDER_REPLICATE)
    M = np.float32([[s, 0, -x0 * s], [0, s, -y0 * s]])
    return cv2.warpAffine(frame, M, (W, H), flags=cv2.INTER_CUBIC, borderMode=cv2.BORDER_REPLICATE)


def cam_at(scene, fmt, p):
    keys = (scene.get("camera") or {}).get(fmt)
    if not keys:
        return None
    if len(keys) == 1:
        return tuple(keys[0])
    a, b = keys[0], keys[-1]
    k = ease(p)
    return tuple(x + (y - x) * k for x, y in zip(a, b))


_badges = {}


def badge(img, fmt, n):
    f = FORMATS[fmt]
    key = (fmt, n)
    if key not in _badges:
        fnt = font("bold", f["badge"])
        text = f"sped up ×{n}"
        px, py = int(f["badge"] * 0.7), int(f["badge"] * 0.35)
        bw, bh = int(fnt.getlength(text) + 2 * px), int(f["badge"] + 2 * py)
        im = Image.new("RGBA", (bw, bh), (0, 0, 0, 0))
        d = ImageDraw.Draw(im)
        d.rounded_rectangle((0, 0, bw - 1, bh - 1), radius=bh // 2, fill=(0, 0, 0, 185), outline=(255, 255, 255, 80))
        d.text((bw / 2, bh / 2), text, font=fnt, fill=(255, 255, 255, 235), anchor="mm")
        a = np.asarray(im).astype(np.float32)
        _badges[key] = (a[..., :3], a[..., 3:4] / 255.0)
    rgb, al = _badges[key]
    h, w = al.shape[:2]
    x, y = 18, 18 if fmt == "9x16" else f["H"] - h - 16
    if fmt == "9x16":
        x = f["W"] - w - 18
    img = img.copy()
    roi = img[y:y + h, x:x + w].astype(np.float32)
    img[y:y + h, x:x + w] = (roi * (1 - al) + rgb * al + 0.5).astype(np.uint8)
    return img


_cards = {}


def card(kind, spec, fmt):
    key = (kind, fmt)
    if key in _cards:
        return _cards[key]
    f = FORMATS[fmt]
    W, H = f["W"], f["H"]
    bg = Image.new("RGB", (W, H), BG)
    glow = Image.new("RGB", (W, H), (48, 30, 28))
    m = Image.new("L", (W, H), 0)
    r = int(max(W, H) * 0.7)
    ImageDraw.Draw(m).ellipse((W - r, -r // 2, W + r // 2, r // 2), fill=255)
    bg.paste(glow, (0, 0), m.filter(ImageFilter.GaussianBlur(r // 4)).point(lambda v: int(v * 0.8)))
    d = ImageDraw.Draw(bg)
    if kind == "title":
        c = spec.d["title_card"]
        tf, sf = font("bold", f["title"] * (0.7 if fmt == "16x9" else 0.62)), font("regular", f["line"])
        lines = wrap(c.get("title", ""), tf, W * 0.82)
        y = H * 0.42 - len(lines) * tf.size * 0.6
        for ln in lines:
            d.text((W / 2, y), ln, font=tf, fill=(255, 255, 255), anchor="mm")
            y += tf.size * 1.2
        bw, bh = int(W * 0.06), max(6, int(f["title"] * 0.08))
        d.rounded_rectangle(((W - bw) / 2, y, (W + bw) / 2, y + bh), radius=bh // 2, fill=ORANGE)
        y += bh + f["line"] * 1.4
        for ln in wrap(c.get("subtitle", ""), sf, W * 0.82):
            d.text((W / 2, y), ln, font=sf, fill=(201, 209, 217), anchor="mm")
            y += sf.size * 1.4
    else:
        c = spec.d["end_card"]
        wf, lf = font("bold", int(f["title"] * 2)), font("regular", f["line"])
        y = H * 0.40
        d.text((W / 2, y), c.get("title", "Blerg"), font=wf, fill=(255, 255, 255), anchor="ms")
        bw, bh = int(f["title"] * 1.2), max(8, int(f["title"] * 0.1))
        y += f["title"] * 0.45
        d.rounded_rectangle(((W - bw) / 2, y, (W + bw) / 2, y + bh), radius=bh // 2, fill=ORANGE)
        y += bh + f["line"] * 2
        for ln in c.get("lines", []):
            d.text((W / 2, y), ln, font=lf, fill=(201, 209, 217), anchor="mm")
            y += lf.size * 1.6
    arr = np.asarray(bg)
    _cards[key] = arr
    return arr


# ---------------------------------------------------------------- captions
def captions(spec, items, fmt):
    out = []
    for it in items:
        if it["kind"] != "scene":
            continue
        sc = spec.scenes[it["i"]]
        caps = sc.get("caption")
        if not caps:
            continue
        caps = caps if isinstance(caps, list) else [caps]
        total = sum(len(c) for c in caps)
        t = it["t0"] + 0.15
        span = it["dur"] - 0.3
        for c in caps:
            d = span * len(c) / total
            pos = sc.get("pos", "bottom")
            out.append((t, t + d, c, pos.get(fmt, "bottom") if isinstance(pos, dict) else pos))
            t += d
    return out


def write_captions(spec, fmt, caps, ass_path, srt_path):
    f = FORMATS[fmt]
    name, _ = ass_font()
    fnt = font("bold", f["cap_size"])
    style = (f"{name},{f['cap_size']},&H00FFFFFF,&H00FFFFFF,&H40000000,&H40000000,-1,0,0,0,100,100,0,0,4,16,0")
    lines = []
    for a, b, text, pos in caps:
        parts = wrap(text, fnt, f["cap_maxw"])
        st = "CapT" if pos == "top" else "Cap"
        lines.append(f"Dialogue: 0,{ass_time(a)},{ass_time(b)},{st},,0,0,0,,{{\\fad(120,160)}}" + "\\N".join(parts))
    top = 120 if fmt == "9x16" else 48
    head = f"""[Script Info]
ScriptType: v4.00+
PlayResX: {f['W']}
PlayResY: {f['H']}
WrapStyle: 2
ScaledBorderAndShadow: yes

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Cap,{style},2,40,40,{f['cap_margin']},1
Style: CapT,{style},8,40,40,{top},1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
"""
    with open(ass_path, "w", encoding="utf-8") as fh:
        fh.write(head + "\n".join(lines) + "\n")
    with open(srt_path, "w", encoding="utf-8") as fh:
        for n, (a, b, text, _) in enumerate(caps, 1):
            fh.write(f"{n}\n{srt_time(a)} --> {srt_time(b)}\n{text}\n\n")


# ---------------------------------------------------------------- audio
def audio(spec, vo, total, path):
    music = spec.asset((spec.d.get("music") or {}).get("file"))
    vol = float((spec.d.get("music") or {}).get("volume", 0.14))
    cmd = ["ffmpeg", "-v", "error", "-y"]
    filt, mix = [], []
    for k, (t, clip) in enumerate(vo):
        cmd += ["-i", clip]
        ms = int(t * 1000)
        filt.append(f"[{k}:a]aresample=48000,aformat=channel_layouts=stereo,adelay={ms}|{ms}[v{k}]")
        mix.append(f"[v{k}]")
    n = len(vo)
    if n:
        filt.append(f"{''.join(mix)}amix=inputs={n}:normalize=0,apad,atrim=0:{total:.3f}[vo]")
    if music:
        cmd += ["-stream_loop", "-1", "-i", music]
        fade = max(0.0, total - 2.0)
        filt.append(f"[{n}:a]aresample=48000,aformat=channel_layouts=stereo,atrim=0:{total:.3f},volume={vol},"
                    f"afade=t=in:d=1.0,afade=t=out:st={fade:.3f}:d=2.0[mu]")
        if n:
            filt.append("[vo]asplit[vo1][vo2]")
            filt.append("[mu][vo2]sidechaincompress=threshold=0.03:ratio=8:attack=20:release=400[duck]")
            filt.append("[vo1][duck]amix=inputs=2:normalize=0[out]")
        else:
            filt.append("[mu]anull[out]")
    elif n:
        filt.append("[vo]anull[out]")
    else:
        run(["ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-t", f"{total:.3f}",
             "-c:a", "pcm_s16le", path])
        return False
    run(cmd + ["-filter_complex", ";".join(filt), "-map", "[out]", "-t", f"{total:.3f}", "-c:a", "pcm_s16le", path])
    return True


# ---------------------------------------------------------------- render
def render(spec, fmt, silent, stills=None):
    f = FORMATS[fmt]
    W, H = f["W"], f["H"]
    take = load_take(spec, spec.d["formats"][fmt]["take"])
    tw, th = take["size"]
    items, total, vo = timeline(spec, fmt, take, silent)
    outdir = os.path.join(OUT, spec.slug)
    os.makedirs(outdir, exist_ok=True)
    base = os.path.join(outdir, f"{spec.slug}-{fmt}")
    caps = captions(spec, items, fmt)
    write_captions(spec, fmt, caps, base + ".ass", base + ".srt")
    for it in items:
        label = it["kind"] if it["kind"] != "scene" else f"scene {it['i']}"
        extra = ""
        if it["kind"] == "scene":
            sp = [f"{a:.1f}-{b:.1f}x{s:.1f}" for a, b, s, _ in it["pieces"]]
            extra = f"  footage {it['foot']:.1f}s  {' '.join(sp)}"
        print(f"  {fmt} {it['t0']:6.2f} +{it['dur']:5.2f}  {label}{extra}")
    print(f"  {fmt} total {total:.1f}s, voice-over clips: {len(vo)}")

    nframes = int(math.ceil(total * FPS))
    reader = Reader(take["path"], tw, th)

    def frame_at(n):
        t = n / FPS
        it = next(x for x in items if x["t0"] <= t < x["t0"] + x["dur"] + 1e-9) if t < total else items[-1]
        local = t - it["t0"]
        if it["kind"] in ("title", "end"):
            img = card(it["kind"], spec, fmt)
            k = min(1.0, local / 0.4, (it["dur"] - local) / 0.5 if it["kind"] == "end" else 1.0)
            return (img.astype(np.float32) * max(0.0, k) + 0.5).astype(np.uint8), it
        st, speed, _ = src_time(it, local)
        src = reader.at(st)
        img = fit(src, W, H, cam_at(spec.scenes[it["i"]], fmt, local / it["dur"]))
        if speed > 1.5:
            img = badge(img, fmt, int(round(speed)))
        return img, it

    if stills:
        for t in stills:
            img, _ = frame_at(int(t * FPS))
            p = f"{base}-still-{t:06.2f}.png"
            Image.fromarray(img).save(p)
            print(f"wrote {p}")
        reader.close()
        return

    wav = base + ".wav"
    has_audio = audio(spec, vo, total, wav)
    ass_name, fontsdir = ass_font()
    out = base + ".mp4"
    enc = subprocess.Popen([
        "ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
        "-f", "rawvideo", "-pix_fmt", "rgb24", "-s", f"{W}x{H}", "-r", str(FPS), "-i", "-", "-i", wav,
        "-vf", f"subtitles={os.path.basename(base)}.ass:fontsdir={fontsdir},format=yuv420p",
        "-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-preset", "medium", "-crf", os.environ.get("CRF", "20"),
        "-profile:v", "high", "-pix_fmt", "yuv420p", "-r", str(FPS), "-c:a", "aac", "-b:a", "192k",
        "-movflags", "+faststart", "-shortest", out], stdin=subprocess.PIPE, cwd=outdir)
    prev_item, prev_last, poster = None, None, None
    hold, xf_start = None, -10 ** 9
    for n in range(nframes):
        img, it = frame_at(n)
        if prev_item is not None and it is not prev_item:
            hold, xf_start = prev_last, n  # dissolve from the previous item's last frame
        raw = img
        if hold is not None and n - xf_start < XF * FPS:
            k = ease((n - xf_start) / (XF * FPS))
            img = (hold.astype(np.float32) * (1 - k) + img.astype(np.float32) * k + 0.5).astype(np.uint8)
        if it["kind"] == "scene":
            poster = raw
        prev_item, prev_last = it, raw
        enc.stdin.write(img.tobytes())
        if n % (FPS * 10) == 0:
            print(f"  {fmt} {n / FPS:5.1f}s", flush=True)
    enc.stdin.close()
    reader.close()
    if enc.wait() != 0:
        sys.exit(f"ffmpeg failed for {fmt}")
    os.remove(wav)
    if poster is not None and fmt == "16x9":
        Image.fromarray(poster).save(os.path.join(outdir, f"{spec.slug}-poster.png"))
    print(f"wrote {out}  ({total:.1f}s, {'voice/music' if has_audio else 'captions only, silent track'})")


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("short")
    ap.add_argument("--format", choices=sorted(FORMATS), action="append")
    ap.add_argument("--silent", action="store_true", help="captions only: no voice-over provider is called")
    ap.add_argument("--voice", choices=["elevenlabs", "command", "files"], help="override the spec's voice provider")
    ap.add_argument("--music", help="override the spec's music file")
    ap.add_argument("--stills", type=float, nargs="+", help="write frames at these output times instead")
    a = ap.parse_args()
    spec = Spec(a.short)
    if a.voice:
        spec.d.setdefault("voice", {})["provider"] = a.voice
    if a.music:
        spec.d.setdefault("music", {})["file"] = os.path.abspath(a.music)
    for fmt in a.format or [f for f in FORMATS if f in spec.d["formats"]]:
        render(spec, fmt, a.silent, a.stills)


if __name__ == "__main__":
    main()
