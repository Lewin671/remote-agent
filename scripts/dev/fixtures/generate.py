#!/usr/bin/env python3
"""Generate numbered test fixtures for remote-agent development.

Produces:
  01_normal.png        - Standard 800x600 RGB PNG image
  02_tall.png          - Tall 400x4000 scrollable PNG
  03_huge_pixels.png   - 8000x8000 (64 megapixel) PNG
  04_large_20mb.png    - Large uncompressed PNG (> 20 MB)
  05_animated.gif      - Animated 2-frame GIF
  06_sample.webp       - Standard WebP image
  07_corrupted.png     - Corrupted PNG with invalid header/data
  08_sample.heic       - HEIC image (generated via macOS sips when available)
  09_long_command.sh   - Long single-line shell command (> 1500 chars)
  10_multiline_heredoc.sh - Multi-line heredoc script with tabs & quotes
"""
import argparse
import os
import shutil
import struct
import subprocess
import sys
import zlib
from pathlib import Path


def png_chunk(tag: bytes, data: bytes) -> bytes:
    return struct.pack(">I", len(data)) + tag + data + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF)


def write_png(path: Path, width: int, height: int, row_generator, compress_level=6):
    """Write an 8-bit RGB PNG file without third-party dependencies."""
    header = b"\x89PNG\r\n\x1a\n"
    ihdr = png_chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))

    compressor = zlib.compressobj(level=compress_level)
    idat_parts = []
    for y in range(height):
        raw_row = row_generator(y, width)
        # 0x00 is filter type None
        filtered = b"\x00" + raw_row
        idat_parts.append(compressor.compress(filtered))
    idat_parts.append(compressor.flush())

    idat = png_chunk(b"IDAT", b"".join(idat_parts))
    iend = png_chunk(b"IEND", b"")

    with open(path, "wb") as f:
        f.write(header + ihdr + idat + iend)


def make_normal_png(path: Path):
    """01_normal.png: 800x600 standard PNG with a color gradient."""
    width, height = 800, 600

    def row_gen(y, w):
        row = bytearray(w * 3)
        gy = int((y / height) * 255)
        for x in range(w):
            gx = int((x / w) * 255)
            row[x * 3] = gx
            row[x * 3 + 1] = gy
            row[x * 3 + 2] = (gx + gy) // 2
        return bytes(row)

    write_png(path, width, height, row_gen, compress_level=6)


def make_tall_png(path: Path):
    """02_tall.png: 400x4000 tall strip with alternating bands."""
    width, height = 400, 4000

    def row_gen(y, w):
        band = (y // 100) % 4
        colors = [
            (50, 120, 220),   # blue
            (220, 80, 60),    # red
            (60, 180, 90),    # green
            (240, 180, 40),   # yellow
        ]
        r, g, b = colors[band]
        return bytes([r, g, b] * w)

    write_png(path, width, height, row_gen, compress_level=6)


def make_huge_pixels_png(path: Path):
    """03_huge_pixels.png: 8000x8000 pixels (64MP) with fast compression."""
    width, height = 8000, 8000
    # Repeating pattern compresses extremely fast
    base_row = bytes([(i % 256) for i in range(width * 3)])

    def row_gen(y, w):
        return base_row

    write_png(path, width, height, row_gen, compress_level=1)


def make_large_20mb_png(path: Path):
    """04_large_20mb.png: PNG file exceeding 20 MiB using level 0 compression."""
    # 3000 x 2500 x 3 bytes RGB = 22,500,000 bytes raw (~21.46 MiB)
    width, height = 3000, 2500

    # Varied pattern to avoid any trivial compression
    def row_gen(y, w):
        row = bytearray(w * 3)
        v = (y * 37) & 0xFF
        for i in range(w * 3):
            row[i] = (v + i) & 0xFF
        return bytes(row)

    write_png(path, width, height, row_gen, compress_level=0)


def make_animated_gif(path: Path):
    """05_animated.gif: 2-frame 200x200 animated GIF using pure Python."""
    # GIF89a header + Logical Screen Descriptor (200x200)
    w, h = 200, 200
    header = b"GIF89a"
    lsd = struct.pack("<HHBBB", w, h, 0x80, 0, 0)  # 2-color global color table
    # Global palette: Color 0 = Red, Color 1 = Blue
    gct = bytes([255, 60, 60, 60, 120, 255])
    # Netscape application extension for infinite looping
    loop_ext = b"\x21\xff\x0bNETSCAPE2.0\x03\x01\x00\x00\x00"

    # Frame 1: all color 0 (Red)
    gce1 = b"\x21\xf9\x04\x00\x32\x00\x00\x00"  # 500ms delay
    img_desc1 = struct.pack("<BHHHHB", 0x2C, 0, 0, w, h, 0)
    # Minimal LZW stream for 40,000 zeroes (color index 0)
    # LZW min code size = 2 (for 2-color table)
    # Clear code = 4, End code = 5
    # Sequence of 40000 0's encoded simply with clear codes
    # For robustness, we can generate a valid simple frame or run sips if available.
    # To be 100% compliant and simple, write uncompressed sub-blocks:
    lzw_data1 = bytearray([0x02])  # LZW min code size 2
    # Simple LZW payload: clear code (100b), zeros, end of info
    # Easier: use sips if available to convert a PNG, or write standard GIF block:
    payload = b"\x04\x20\x84\x23\x05"  # Valid minimal 2x2 LZW block
    # Actually, let's make a crisp 100x100 GIF:
    # If sips is available on macOS, use sips to produce standard GIF:
    if shutil.which("sips"):
        tmp_png = path.with_suffix(".tmp.png")
        write_png(tmp_png, 200, 200, lambda y, w: bytes([255, 100, 100] * w), compress_level=1)
        subprocess.run(["sips", "-s", "format", "gif", str(tmp_png), "--out", str(path)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        tmp_png.unlink(missing_ok=True)
        if path.exists():
            return

    # Fallback minimal valid GIF89a
    # 2x2 GIF with 2 colors:
    gif_bytes = (
        b"GIF89a\x02\x00\x02\x00\x80\x00\x00\xff\x00\x00\x00\x00\xff"
        b"\x21\xff\x0bNETSCAPE2.0\x03\x01\x00\x00\x00"
        b"\x21\xf9\x04\x00\x32\x00\x00\x00\x2c\x00\x00\x00\x00\x02\x00\x02\x00\x00\x02\x02\x44\x01\x00"
        b"\x21\xf9\x04\x00\x32\x00\x00\x00\x2c\x00\x00\x00\x00\x02\x00\x02\x00\x00\x02\x02\x4c\x01\x00"
        b"\x3b"
    )
    path.write_bytes(gif_bytes)


def make_webp(path: Path, src_png: Path):
    """06_sample.webp: WebP image generated from normal PNG or minimal RIFF."""
    if shutil.which("cwebp"):
        subprocess.run(["cwebp", "-quiet", "-q", "80", str(src_png), "-o", str(path)], check=False)
        if path.exists() and path.stat().st_size > 0:
            return

    if shutil.which("ffmpeg"):
        subprocess.run(["ffmpeg", "-y", "-i", str(src_png), "-vcodec", "libwebp", str(path)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        if path.exists() and path.stat().st_size > 0:
            return

    # Fallback minimal valid 1x1 WebP
    # RIFF header + WEBP + VP8 chunk
    vp8_data = b"\x30\x01\x00\x9d\x01\x2a\x01\x00\x01\x00\x00\x34\x25\xa2\x00\x03\x70\x00\xfe\xfb\xfd\x50\x00"
    riff_header = b"RIFF" + struct.pack("<I", 12 + len(vp8_data)) + b"WEBPVP8 " + struct.pack("<I", len(vp8_data)) + vp8_data
    path.write_bytes(riff_header)


def make_corrupted_png(path: Path):
    """07_corrupted.png: Invalid PNG file (starts with signature then garbage)."""
    corrupted = b"\x89PNG\r\n\x1a\n" + b"\x00\x00\x00\x0dIHDR_TRUNCATED_CORRUPTED_BYTES" + os.urandom(256)
    path.write_bytes(corrupted)


def make_heic(path: Path, src_png: Path):
    """08_sample.heic: HEIC image generated via macOS sips when available."""
    if shutil.which("sips"):
        res = subprocess.run(["sips", "-s", "format", "heic", str(src_png), "--out", str(path)],
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        if res.returncode == 0 and path.exists():
            return True
    return False


def make_long_command(path: Path):
    """09_long_command.sh: Single-line command exceeding 1500 characters."""
    args = [f"--param-{i}=value_with_arbitrary_string_{i*7}_alpha_beta_gamma" for i in range(35)]
    flags = " ".join(args)
    cmd = (
        f'echo "=== START PIPELINE ===" && '
        f'env RA_DEV_PROBE=1 REMOTE_AGENT_TEST_TAG="probe-line-length-verification" '
        f'python3 -c \'import sys, os; print("PROBE: received arguments count =", len(sys.argv))\' {flags} | '
        f'awk \'{{ print toupper($0); print "LENGTH=" length($0); }}\' | '
        f'grep -E "(PROBE|LENGTH|START)" | '
        f'sed -e "s/PROBE/VERIFIED_OK/g" -e "s/START/COMPLETED/g" | '
        f'while IFS= read -r line; do printf "[PIPELINE_OUTPUT] %s\\n" "$line"; done && '
        f'echo "=== END PIPELINE: SUCCESS ==="\n'
    )
    path.write_text(cmd)


def make_multiline_heredoc(path: Path):
    """10_multiline_heredoc.sh: Complex multi-line heredoc script with tabs & quotes."""
    content = """#!/bin/sh
# Sample script demonstrating multi-line heredoc input with quotes and indentation.
set -eu

OUTPUT_DIR="${1:-/tmp/ra-heredoc-output}"
mkdir -p "$OUTPUT_DIR"

cat << 'HEREDOC_EOF' > "$OUTPUT_DIR/sample_config.toml"
# remote-agent dev task configuration fixture
[session]
name = "fixture-eval-session"
version = "v1.0-alpha"
enabled = true
tags = ["fast", "isolated", "fixtures"]

[cli.options]
approval_policy = "on-request"
sandbox_mode = "workspace-write"
approvals_reviewer = "user"
timeout_seconds = 300

[environment.variables]
PATH = "/usr/local/bin:/usr/bin:/bin"
LANG = "en_US.UTF-8"
REMOTE_AGENT_TMUX_SOCKET = "ra-isolated-test"

[complex_data]
multiline_description = '''
This is a multiline description string within the heredoc.
It tests whether terminal copy-pasting, bracketed paste mode,
and agent input injectors properly handle embedded newline characters
without premature submission or escaping corruption.
'''

tab_indented_matrix = [
	["col_a", "col_b", "col_c"],
	["100.01", "200.02", "300.03"],
	["status: ok", "state: clean", "verified: true"]
]
HEREDOC_EOF

echo "Heredoc written to $OUTPUT_DIR/sample_config.toml"
cat "$OUTPUT_DIR/sample_config.toml"
"""
    path.write_text(content)


def main():
    parser = argparse.ArgumentParser(description="Generate numbered test fixtures for remote-agent development.")
    parser.add_argument("out_dir", nargs="?", default="./fixtures", help="Directory where fixtures will be written (default: ./fixtures)")
    args = parser.parse_args()

    out = Path(args.out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    print(f"Generating test fixtures in: {out}")

    # 1. Normal PNG
    p1 = out / "01_normal.png"
    make_normal_png(p1)
    print(f"  [+] 01_normal.png: {p1.stat().st_size:,} bytes (800x600)")

    # 2. Tall PNG
    p2 = out / "02_tall.png"
    make_tall_png(p2)
    print(f"  [+] 02_tall.png: {p2.stat().st_size:,} bytes (400x4000)")

    # 3. Huge pixels PNG
    p3 = out / "03_huge_pixels.png"
    make_huge_pixels_png(p3)
    print(f"  [+] 03_huge_pixels.png: {p3.stat().st_size:,} bytes (8000x8000)")

    # 4. Large >20MB PNG
    p4 = out / "04_large_20mb.png"
    make_large_20mb_png(p4)
    print(f"  [+] 04_large_20mb.png: {p4.stat().st_size:,} bytes ({p4.stat().st_size / (1024*1024):.2f} MiB > 20 MiB)")

    # 5. Animated GIF
    p5 = out / "05_animated.gif"
    make_animated_gif(p5)
    print(f"  [+] 05_animated.gif: {p5.stat().st_size:,} bytes")

    # 6. WebP
    p6 = out / "06_sample.webp"
    make_webp(p6, p1)
    print(f"  [+] 06_sample.webp: {p6.stat().st_size:,} bytes")

    # 7. Corrupted PNG
    p7 = out / "07_corrupted.png"
    make_corrupted_png(p7)
    print(f"  [+] 07_corrupted.png: {p7.stat().st_size:,} bytes (intentionally invalid)")

    # 8. HEIC
    p8 = out / "08_sample.heic"
    if make_heic(p8, p1):
        print(f"  [+] 08_sample.heic: {p8.stat().st_size:,} bytes")
    else:
        print(f"  [-] 08_sample.heic: skipped (sips HEIC conversion not available)")

    # 9. Long single-line command
    p9 = out / "09_long_command.sh"
    make_long_command(p9)
    print(f"  [+] 09_long_command.sh: {p9.stat().st_size:,} chars single-line command")

    # 10. Multi-line heredoc
    p10 = out / "10_multiline_heredoc.sh"
    make_multiline_heredoc(p10)
    print(f"  [+] 10_multiline_heredoc.sh: {p10.stat().st_size:,} bytes multi-line heredoc")

    print("\nAll fixtures generated successfully.")


if __name__ == "__main__":
    main()
