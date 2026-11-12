#!/usr/bin/env python3
"""Tests for remote-agent development tools in scripts/dev/."""
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class DevToolsTest(unittest.TestCase):
    def test_preflight(self):
        script = ROOT / "scripts/dev/preflight.sh"
        self.assertTrue(script.is_file())
        with tempfile.TemporaryDirectory(dir="/tmp", prefix="rad-test-") as temp_dir:
            env = os.environ.copy()
            # Clean polluting vars for test
            for k in ["REMOTE_AGENT_HOME", "REMOTE_AGENT_TMUX_SOCKET", "REMOTE_AGENT_SERVER", "RA_DEV_AUTHORITY"]:
                env.pop(k, None)
            env["RA_DEV_DIR"] = temp_dir
            proc = subprocess.run([str(script), "--dir", temp_dir], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env)
            self.assertEqual(proc.returncode, 0, f"preflight failed: {proc.stdout}\n{proc.stderr}")
            self.assertIn("RA_DEV_DIR:", proc.stdout)
            self.assertIn("Loopback port bind:", proc.stdout)

        # Verify that an overly long path fails
        long_dir = "/tmp/a_very_long_path_that_exceeds_seventy_characters_for_socket_bounds_test_directory"
        proc_long = subprocess.run([str(script), "--dir", long_dir], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.assertNotEqual(proc_long.returncode, 0)
        self.assertIn("RA_DEV_DIR path length", proc_long.stdout)

    def test_fixtures_generator(self):
        script = ROOT / "scripts/dev/fixtures.sh"
        self.assertTrue(script.is_file())
        with tempfile.TemporaryDirectory(prefix="fixtures-test-") as temp_dir:
            proc = subprocess.run([str(script), temp_dir], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            self.assertEqual(proc.returncode, 0, f"fixtures failed: {proc.stdout}\n{proc.stderr}")

            out = Path(temp_dir)
            p_normal = out / "01_normal.png"
            p_tall = out / "02_tall.png"
            p_huge = out / "03_huge_pixels.png"
            p_large = out / "04_large_20mb.png"
            p_gif = out / "05_animated.gif"
            p_webp = out / "06_sample.webp"
            p_corrupt = out / "07_corrupted.png"
            p_long = out / "09_long_command.sh"
            p_heredoc = out / "10_multiline_heredoc.sh"

            self.assertTrue(p_normal.exists())
            self.assertTrue(p_tall.exists())
            self.assertTrue(p_huge.exists())
            self.assertTrue(p_large.exists())
            self.assertGreater(p_large.stat().st_size, 20 * 1024 * 1024, "04_large_20mb.png must exceed 20MB")
            self.assertTrue(p_gif.exists())
            self.assertTrue(p_webp.exists())
            self.assertTrue(p_corrupt.exists())
            self.assertTrue(p_long.exists())
            self.assertGreater(len(p_long.read_text()), 1500, "09_long_command.sh must exceed 1500 chars")
            self.assertTrue(p_heredoc.exists())
            self.assertIn("HEREDOC_EOF", p_heredoc.read_text())

    def test_frames_extraction(self):
        if not shutil.which("ffmpeg") or not shutil.which("ffprobe"):
            self.skipTest("ffmpeg/ffprobe not installed")

        script = ROOT / "scripts/dev/frames.sh"
        self.assertTrue(script.is_file())

        with tempfile.TemporaryDirectory(prefix="frames-test-") as temp_dir:
            temp_path = Path(temp_dir)
            video = temp_path / "test.mp4"
            out_dir = temp_path / "out"

            # Generate synthetic 3-second test clip
            cmd_gen = [
                "ffmpeg", "-y", "-v", "error",
                "-f", "lavfi", "-i", "testsrc=duration=3:size=320x240:rate=20",
                str(video),
            ]
            subprocess.run(cmd_gen, check=True)

            # Run frames.sh with timestamps 0.8 and 2.1
            proc = subprocess.run([str(script), str(video), str(out_dir), "0.8", "2.1"],
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            self.assertEqual(proc.returncode, 0, f"frames.sh failed: {proc.stdout}\n{proc.stderr}")

            summary_file = out_dir / "summary.json"
            self.assertTrue(summary_file.exists())
            summary = json.loads(summary_file.read_text())
            self.assertEqual(summary["video"], "test.mp4")
            self.assertGreater(summary["contact_sheet"]["frame_count"], 0)
            self.assertEqual(len(summary["points"]), 2)

            contact_sheet = out_dir / "contact" / "sheet.jpg"
            self.assertTrue(contact_sheet.exists())
            self.assertTrue((out_dir / "points" / "01_0.800s" / "point.json").exists())
            self.assertTrue((out_dir / "points" / "02_2.100s" / "point.json").exists())


if __name__ == "__main__":
    unittest.main()
