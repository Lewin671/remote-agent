#!/usr/bin/env python3
"""Regenerates the JPEG fixtures for images.test.ts.

Needs libjpeg-turbo's cjpeg and jpegtran, and cwebp, on PATH (plus macOS
sips for the WebP source). The jpegtran outputs are
the oracle for the lossless rotation: the test decodes them and checks the
coefficients match ours exactly. Run from this directory:

    python3 make.py
"""
import os
import random
import subprocess

HERE = os.path.dirname(os.path.abspath(__file__))


def ppm(path, w, h, seed):
    # Gradients plus noise, so every block has AC energy in both directions
    # and a wrong sign or transpose shows up in the coefficients.
    rnd = random.Random(seed)
    px = bytearray()
    for y in range(h):
        for x in range(w):
            px += bytes(
                (
                    (x * 255 // max(w - 1, 1) + rnd.randrange(40)) % 256,
                    (y * 255 // max(h - 1, 1) + rnd.randrange(40)) % 256,
                    ((x * y) % 256 + rnd.randrange(40)) % 256,
                )
            )
    with open(path, "wb") as f:
        f.write(b"P6\n%d %d\n255\n" % (w, h) + px)


def cjpeg(name, w, h, *args):
    src = os.path.join(HERE, name + ".ppm")
    ppm(src, w, h, name)
    out = os.path.join(HERE, name + ".jpg")
    subprocess.run(["cjpeg", "-quality", "80", *args, "-outfile", out, src], check=True)
    os.remove(src)


# jpegtran transform for each EXIF orientation.
TRANSFORMS = {
    2: ["-flip", "horizontal"],
    3: ["-rotate", "180"],
    4: ["-flip", "vertical"],
    5: ["-transpose"],
    6: ["-rotate", "90"],
    7: ["-transverse"],
    8: ["-rotate", "270"],
}


def jpegtran(name, orientation, required=True):
    src = os.path.join(HERE, name + ".jpg")
    out = os.path.join(HERE, "%s-o%d.jpg" % (name, orientation))
    r = subprocess.run(
        ["jpegtran", "-perfect", "-copy", "none", *TRANSFORMS[orientation], "-outfile", out, src],
        capture_output=True,
    )
    if r.returncode != 0:
        if os.path.exists(out):
            os.remove(out)
        if required:
            raise SystemExit("jpegtran failed for %s o%d" % (name, orientation))
        return False
    return True


cjpeg("s420", 48, 32, "-sample", "2x2")
cjpeg("s422", 32, 16, "-sample", "2x1")
cjpeg("s444", 24, 16, "-sample", "1x1", "-optimize")
cjpeg("gray", 24, 16, "-grayscale")
cjpeg("restart", 64, 48, "-sample", "2x2", "-restart", "1B")
cjpeg("odd420", 40, 32, "-sample", "2x2")
cjpeg("prog", 32, 32, "-progressive")
cjpeg("arith", 32, 32, "-arithmetic")

for o in TRANSFORMS:
    jpegtran("s420", o)
    jpegtran("s422", o)
# 40 wide is not a multiple of the 16px MCU: rotating 90 keeps the partial
# column at the bottom, so it is perfect; 270 would move it and is not.
jpegtran("odd420", 6)
assert not jpegtran("odd420", 8, required=False)

# A 3x2 lossless WebP; the tests wrap it in VP8X with EXIF and XMP chunks.
src = os.path.join(HERE, "tiny.ppm")
with open(src, "wb") as f:
    f.write(b"P6\n3 2\n255\n" + bytes([255, 0, 0, 0, 255, 0, 0, 0, 255, 255, 255, 0, 0, 255, 255, 255, 0, 255]))
png = os.path.join(HERE, "tiny.png")
subprocess.run(["sips", "-s", "format", "png", src, "--out", png], check=True, capture_output=True)
subprocess.run(["cwebp", "-quiet", "-lossless", png, "-o", os.path.join(HERE, "tiny.webp")], check=True)
os.remove(src)
os.remove(png)
