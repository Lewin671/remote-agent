#!/usr/bin/env python3
"""Tests for scripts/verify_apk.py."""
import importlib.util
from pathlib import Path
import struct
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location("verify_apk", Path(__file__).with_name("verify_apk.py"))
verify_apk = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify_apk)


def _encode_uleb128(value: int) -> bytes:
    out = bytearray()
    while True:
        byte = value & 0x7F
        value >>= 7
        if value:
            out.append(byte | 0x80)
        else:
            out.append(byte)
            return bytes(out)


def _build_minimal_dex(descriptors: list[str]) -> bytes:
    """Build a minimal valid little-endian DEX byte image containing the given class_defs."""
    header_size = 112
    n = len(descriptors)
    string_ids_off = header_size
    type_ids_off = string_ids_off + n * 4
    class_defs_off = type_ids_off + n * 4
    data_off = class_defs_off + n * 32

    string_blobs = bytearray()
    string_offs = []
    for desc in descriptors:
        string_offs.append(data_off + len(string_blobs))
        encoded = desc.encode("utf-8")
        string_blobs += _encode_uleb128(len(encoded)) + encoded + b"\x00"

    buf = bytearray(data_off + len(string_blobs))
    buf[0:8] = b"dex\n035\x00"
    struct.pack_into("<III", buf, 32, len(buf), header_size, verify_apk.DEX_ENDIAN_CONSTANT)
    struct.pack_into("<IIII", buf, 56, n, string_ids_off, n, type_ids_off)
    struct.pack_into("<II", buf, 96, n, class_defs_off)
    for i, off in enumerate(string_offs):
        struct.pack_into("<I", buf, string_ids_off + i * 4, off)
        struct.pack_into("<I", buf, type_ids_off + i * 4, i)
        struct.pack_into("<I", buf, class_defs_off + i * 32, i)
    buf[data_off:] = string_blobs
    return bytes(buf)


def _write_apk(path: Path, dex_map: dict[str, bytes]) -> None:
    with zipfile.ZipFile(path, "w") as archive:
        for name, content in dex_map.items():
            archive.writestr(name, content)


class VerifyApkTests(unittest.TestCase):
    def test_accepts_apk_with_all_required_classes_across_multidex(self):
        with tempfile.TemporaryDirectory() as tmp:
            apk = Path(tmp) / "ok.apk"
            half = len(verify_apk.REQUIRED_CLASSES) // 2
            dex1 = _build_minimal_dex(list(verify_apk.REQUIRED_CLASSES[:half]))
            dex2 = _build_minimal_dex(list(verify_apk.REQUIRED_CLASSES[half:]))
            _write_apk(apk, {"classes.dex": dex1, "classes2.dex": dex2})
            dex_count, class_count = verify_apk.verify_apk(apk)
            self.assertEqual(dex_count, 2)
            self.assertEqual(class_count, len(verify_apk.REQUIRED_CLASSES))
            self.assertEqual(verify_apk.main([str(apk)]), 0)

    def test_rejects_apk_missing_required_spi(self):
        with tempfile.TemporaryDirectory() as tmp:
            apk = Path(tmp) / "stripped.apk"
            remaining = [c for c in verify_apk.REQUIRED_CLASSES if "WebRtcClassLoader" not in c]
            _write_apk(apk, {"classes.dex": _build_minimal_dex(remaining)})
            with self.assertRaises(ValueError) as ctx:
                verify_apk.verify_apk(apk)
            self.assertIn("org/webrtc/WebRtcClassLoader", str(ctx.exception))
            self.assertEqual(verify_apk.main([str(apk)]), 1)

    def test_rejects_apk_without_dex(self):
        with tempfile.TemporaryDirectory() as tmp:
            apk = Path(tmp) / "empty.apk"
            _write_apk(apk, {"AndroidManifest.xml": b"binary"})
            self.assertEqual(verify_apk.main([str(apk)]), 1)


if __name__ == "__main__":
    unittest.main()
