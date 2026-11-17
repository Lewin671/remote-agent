#!/usr/bin/env python3
"""Verify a release APK keeps BouncyCastle JCA SPI classes after R8 shrinking.

Usage: scripts/verify_apk.py [APK_PATH]

Without APK_PATH, checks android/app/build/outputs/apk/release/app-release.apk.
"""
import argparse
from pathlib import Path
import re
import struct
import sys
import zipfile

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_APK = ROOT / "android/app/build/outputs/apk/release/app-release.apk"
DEX_FILE = re.compile(r"classes\d*\.dex")
DEX_ENDIAN_CONSTANT = 0x12345678

# Classes looked up by name at runtime that R8 would strip without keep rules:
# BouncyCastle's Ed25519/X25519 JCA SPI (Wire.java; missing ones made Direct
# never authorize) and the classes libjingle_peerconnection's JNI_OnLoad finds
# (missing ones abort the process when Direct first loads WebRTC).
REQUIRED_CLASSES = (
    "Lorg/bouncycastle/jce/provider/BouncyCastleProvider;",
    "Lorg/bouncycastle/jcajce/provider/asymmetric/EdEC$Mappings;",
    "Lorg/bouncycastle/jcajce/provider/asymmetric/edec/KeyFactorySpi;",
    "Lorg/bouncycastle/jcajce/provider/asymmetric/edec/KeyFactorySpi$Ed25519;",
    "Lorg/bouncycastle/jcajce/provider/asymmetric/edec/KeyFactorySpi$X25519;",
    "Lorg/bouncycastle/jcajce/provider/asymmetric/edec/KeyAgreementSpi;",
    "Lorg/bouncycastle/jcajce/provider/asymmetric/edec/KeyAgreementSpi$X25519;",
    "Lorg/bouncycastle/jcajce/provider/asymmetric/edec/SignatureSpi;",
    "Lorg/bouncycastle/jcajce/provider/asymmetric/edec/SignatureSpi$Ed25519;",
    "Lorg/bouncycastle/jcajce/provider/asymmetric/edec/BCEdDSAPrivateKey;",
    "Lorg/bouncycastle/jcajce/provider/asymmetric/edec/BCEdDSAPublicKey;",
    "Lorg/bouncycastle/jcajce/provider/asymmetric/edec/BCXDHPrivateKey;",
    "Lorg/bouncycastle/jcajce/provider/asymmetric/edec/BCXDHPublicKey;",
    "Lorg/jni_zero/JniZero;",
    "Lorg/webrtc/WebRtcClassLoader;",
)


def _read_dex_string(data: bytes, string_ids_off: int, string_ids_size: int, idx: int) -> str:
    if not 0 <= idx < string_ids_size:
        raise ValueError(f"DEX string index out of range: {idx}")
    (str_data_off,) = struct.unpack_from("<I", data, string_ids_off + idx * 4)
    pos = str_data_off
    for _ in range(5):
        if pos >= len(data):
            raise ValueError("Truncated ULEB128 string length in DEX")
        byte = data[pos]
        pos += 1
        if (byte & 0x80) == 0:
            break
    else:
        raise ValueError("Invalid ULEB128 string length in DEX")
    end = data.find(0, pos)
    if end == -1:
        raise ValueError("Unterminated string in DEX")
    return data[pos:end].decode("utf-8", errors="replace")


def dex_class_descriptors(data: bytes) -> set[str]:
    """Return all class_def JVM descriptors (e.g. 'Lcom/example/Foo;') from one DEX."""
    if len(data) < 112 or data[:4] != b"dex\n":
        raise ValueError("Invalid DEX header")
    (endian,) = struct.unpack_from("<I", data, 40)
    if endian != DEX_ENDIAN_CONSTANT:
        raise ValueError(f"Unsupported DEX endianness: 0x{endian:08x}")
    str_size, str_off, type_size, type_off = struct.unpack_from("<IIII", data, 56)
    cls_size, cls_off = struct.unpack_from("<II", data, 96)
    classes: set[str] = set()
    for i in range(cls_size):
        (type_idx,) = struct.unpack_from("<I", data, cls_off + i * 32)
        if not 0 <= type_idx < type_size:
            raise ValueError(f"DEX type index out of range: {type_idx}")
        (descriptor_idx,) = struct.unpack_from("<I", data, type_off + type_idx * 4)
        classes.add(_read_dex_string(data, str_off, str_size, descriptor_idx))
    return classes


def verify_apk(apk_path: Path, required: tuple[str, ...] = REQUIRED_CLASSES) -> tuple[int, int]:
    """Inspect all classes*.dex in apk_path and raise ValueError if any required class is missing."""
    if not apk_path.is_file():
        raise FileNotFoundError(f"APK not found: {apk_path}")
    classes: set[str] = set()
    dex_count = 0
    with zipfile.ZipFile(apk_path) as archive:
        for name in sorted(archive.namelist()):
            if DEX_FILE.fullmatch(name):
                dex_count += 1
                classes |= dex_class_descriptors(archive.read(name))
    if dex_count == 0:
        raise ValueError(f"No classes*.dex found in {apk_path}")
    missing = [cls for cls in required if cls not in classes]
    if missing:
        formatted = ", ".join(cls[1:-1] if cls.startswith("L") and cls.endswith(";") else cls for cls in missing)
        raise ValueError(f"{apk_path} is missing reflectively loaded classes after R8: {formatted}")
    return dex_count, len(classes)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("apk", nargs="?", type=Path, default=DEFAULT_APK,
                        help="Path to the release APK (default: android/app/build/outputs/apk/release/app-release.apk)")
    args = parser.parse_args(argv)
    try:
        dex_count, class_count = verify_apk(args.apk)
    except (OSError, zipfile.BadZipFile, struct.error, ValueError) as exc:
        print(f"verify_apk: {exc}", file=sys.stderr)
        return 1
    print(f"verify_apk: OK ({class_count} classes across {dex_count} dex file(s); {len(REQUIRED_CLASSES)} required reflectively loaded classes present)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
