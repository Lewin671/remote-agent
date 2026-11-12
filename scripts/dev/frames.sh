#!/bin/sh
# frames.sh: extract 2fps contact sheet and full-rate point-in-time frame snippets
# from a screen recording using ffmpeg.
# Usage: scripts/dev/frames.sh <video> [output_dir] [timestamp1 timestamp2 ...]
exec python3 - "$@" << 'PYEOF'
import argparse
import json
import math
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path


def die(msg):
    print(f"frames.sh: error: {msg}", file=sys.stderr)
    sys.exit(1)


def parse_timestamp(s: str) -> float:
    """Parse timestamp in seconds (e.g. 1.5, 90, 01:23, 00:01:23.500)."""
    s = s.strip()
    if not s:
        raise ValueError("empty timestamp")
    parts = s.split(":")
    if len(parts) == 1:
        return float(parts[0])
    elif len(parts) == 2:
        minutes, seconds = parts
        return float(minutes) * 60 + float(seconds)
    elif len(parts) == 3:
        hours, minutes, seconds = parts
        return float(hours) * 3600 + float(minutes) * 60 + float(seconds)
    else:
        raise ValueError(f"unrecognized timestamp format: {s}")


def get_video_info(video_path: Path):
    if not shutil.which("ffprobe"):
        die("ffprobe is required but not installed or not in PATH")

    cmd = [
        "ffprobe", "-v", "error",
        "-select_streams", "v:0",
        "-show_entries", "stream=width,height,r_frame_rate,duration,nb_frames:format=duration",
        "-of", "json",
        str(video_path),
    ]
    proc = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
    if proc.returncode != 0:
        die(f"ffprobe failed: {proc.stderr.strip()}")

    try:
        data = json.loads(proc.stdout)
    except Exception as e:
        die(f"failed to parse ffprobe json: {e}")

    stream = data.get("streams", [{}])[0]
    fmt = data.get("format", {})

    dur_str = stream.get("duration") or fmt.get("duration") or "0"
    duration = float(dur_str)

    fps_eval = 30.0
    fps_raw = stream.get("r_frame_rate", "30/1")
    if "/" in fps_raw:
        num, den = fps_raw.split("/")
        if float(den) > 0:
            fps_eval = float(num) / float(den)

    width = int(stream.get("width", 0))
    height = int(stream.get("height", 0))

    return {
        "duration": duration,
        "fps": fps_eval,
        "width": width,
        "height": height,
    }


def main():
    parser = argparse.ArgumentParser(description="Extract 2fps contact sheets and point-in-time frame clips from video.")
    parser.add_argument("video", help="Path to input video / screen recording file")
    parser.add_argument("out_dir", nargs="?", default="", help="Output directory (default: <video_basename>_frames)")
    parser.add_argument("timestamps", nargs="*", help="Timestamps to extract around (seconds or mm:ss or hh:mm:ss)")
    parser.add_argument("--window", type=float, default=0.5, help="Half-window in seconds around timestamp (default: 0.5s, total 1.0s)")
    parser.add_argument("--fps", type=float, default=2.0, help="Contact sheet frame rate (default: 2.0 fps)")
    args = parser.parse_args()

    video = Path(args.video).resolve()
    if not video.is_file():
        die(f"video file not found: {video}")

    if not shutil.which("ffmpeg"):
        die("ffmpeg is required but not installed or not in PATH")

    out_base = Path(args.out_dir).resolve() if args.out_dir else video.parent / f"{video.stem}_frames"
    contact_dir = out_base / "contact"
    contact_frames_dir = contact_dir / "frames"
    points_base_dir = out_base / "points"

    contact_frames_dir.mkdir(parents=True, exist_ok=True)
    points_base_dir.mkdir(parents=True, exist_ok=True)

    info = get_video_info(video)
    duration = info["duration"]
    print(f"==> Video: {video.name} ({duration:.2f}s, {info['width']}x{info['height']}, {info['fps']:.1f} fps)")
    print(f"==> Output directory: {out_base}")

    # 1. Extract 2fps overview frames
    print(f"==> [1/3] Extracting contact frames at {args.fps} fps...")
    cmd_contact_frames = [
        "ffmpeg", "-y", "-v", "error",
        "-i", str(video),
        "-vf", f"fps={args.fps}",
        "-q:v", "2",
        str(contact_frames_dir / "frame_%04d.jpg"),
    ]
    subprocess.run(cmd_contact_frames, check=True)
    contact_frame_count = len(list(contact_frames_dir.glob("frame_*.jpg")))
    print(f"    Extracted {contact_frame_count} overview frames.")

    # 2. Build contact sheet
    print(f"==> [2/3] Generating contact sheet...")
    # Determine grid size (e.g. 4 columns)
    cols = 4
    rows = max(1, math.ceil(max(1, contact_frame_count) / cols))
    sheet_path = contact_dir / "sheet.jpg"
    cmd_sheet = [
        "ffmpeg", "-y", "-v", "error",
        "-i", str(video),
        "-vf", f"fps={args.fps},scale=320:-1,tile={cols}x{rows}",
        "-frames:v", "1",
        "-q:v", "2",
        str(sheet_path),
    ]
    subprocess.run(cmd_sheet, check=False)
    if sheet_path.exists():
        print(f"    Generated contact sheet: {sheet_path}")

    # 3. Extract point snippets
    parsed_points = []
    for idx, raw_ts in enumerate(args.timestamps, start=1):
        try:
            ts_sec = parse_timestamp(raw_ts)
        except ValueError as e:
            print(f"warning: skipping invalid timestamp '{raw_ts}': {e}", file=sys.stderr)
            continue

        start_t = max(0.0, ts_sec - args.window)
        dur_t = min(duration - start_t, args.window * 2.0) if duration > 0 else (args.window * 2.0)
        pt_label = f"{idx:02d}_{ts_sec:.3f}s"
        pt_dir = points_base_dir / pt_label
        pt_dir.mkdir(parents=True, exist_ok=True)

        cmd_point = [
            "ffmpeg", "-y", "-v", "error",
            "-ss", f"{start_t:.3f}",
            "-t", f"{dur_t:.3f}",
            "-i", str(video),
            "-q:v", "2",
            str(pt_dir / "frame_%04d.jpg"),
        ]
        subprocess.run(cmd_point, check=True)
        pt_frames = len(list(pt_dir.glob("frame_*.jpg")))

        pt_meta = {
            "index": idx,
            "target_timestamp": ts_sec,
            "start_time": start_t,
            "duration": dur_t,
            "frame_count": pt_frames,
            "directory": str(pt_dir.relative_to(out_base)),
        }
        (pt_dir / "point.json").write_text(json.dumps(pt_meta, indent=2))
        parsed_points.append(pt_meta)
        print(f"    Point #{idx} at {ts_sec:.3f}s [{start_t:.3f}s - {start_t+dur_t:.3f}s]: {pt_frames} frames in {pt_dir.name}")

    # Write summary.json
    summary = {
        "video": video.name,
        "video_info": info,
        "contact_sheet": {
            "fps": args.fps,
            "frame_count": contact_frame_count,
            "sheet": str(sheet_path.relative_to(out_base)) if sheet_path.exists() else None,
            "frames_dir": str(contact_frames_dir.relative_to(out_base)),
        },
        "points": parsed_points,
    }
    (out_base / "summary.json").write_text(json.dumps(summary, indent=2))
    print(f"==> Finished. Summary saved to {out_base / 'summary.json'}")


if __name__ == "__main__":
    main()
PYEOF
