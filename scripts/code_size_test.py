"""Counting rules of scripts/dev/code_size.py: what is product, test or ignored."""
import importlib.util
import io
import json
import subprocess
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "dev" / "code_size.py"
spec = importlib.util.spec_from_file_location("code_size", SCRIPT)
code_size = importlib.util.module_from_spec(spec)
spec.loader.exec_module(code_size)


class RulesTest(unittest.TestCase):
    def test_classification(self):
        cases = {
            "web/src/app.tsx": ("web", "product"),
            "web/src/transport/peer/wire.js": ("web", "product"),
            "web/src/app.test.tsx": ("web", "test"),
            "web/src/gen/x.ts": None,
            "web/src/i18n/generated.ts": None,
            "web/src/dev/gallery.tsx": None,
            "web/service-worker.ts": None,
            "android/app/src/main/java/A.kt": ("android", "product"),
            "android/core/src/main/java/A.java": ("android", "product"),
            "android/app/src/test/java/ATest.kt": ("android", "test"),
            "android/app/src/androidTest/java/ATest.kt": ("android", "test"),
            "android/app/src/main/gen/A.kt": None,
            "android/app/src/main/java/I18nGenerated.kt": None,
            "ios/RemoteAgent/UI/V.swift": ("ios", "product"),
            "ios/RemoteAgentCore/Tests/RACoreTests/T.swift": ("ios", "test"),
            "ios/RemoteAgentUITests/T.swift": ("ios", "test"),
            "ios/RemoteAgentCore/.build/x/Y.swift": None,
            "ios/RemoteAgentCore/Sources/RAProto/a.pb.swift": None,
            "ios/RemoteAgent/Strings+Generated.swift": None,
            "clientcore/core/a.go": ("clientcore", "product"),
            "clientcore/core/a_test.go": ("clientcore", "test"),
            "clientcore/gen/a.go": None,
            "internal/daemon/a.go": ("root-go", "product"),
            "cmd/remote-agent/a_test.go": ("root-go", "test"),
            "internal/protocol/a.pb.go": None,
            "gen/remoteagent/core/v1/app.pb.go": None,
            "docs/x.go": None,
        }
        for path, expected in cases.items():
            with self.subTest(path=path):
                self.assertEqual(code_size.classify(path), expected)

    def test_worktree_and_ref_agree_and_delta(self):
        files = {"web/src/a.ts": "1\n2\n3", "web/src/a.test.ts": "x\n", "clientcore/a.go": "p\nq\n",
                 "clientcore/a_test.go": "t\n", "web/src/gen/g.ts": "ignored\n"}
        with tempfile.TemporaryDirectory(prefix="code-size-") as temp:
            root = Path(temp)
            for name, text in files.items():
                (root / name).parent.mkdir(parents=True, exist_ok=True)
                (root / name).write_text(text)
            git = lambda *a: subprocess.run(["git", "-C", temp, "-c", "user.name=t", "-c", "user.email=t@example.com", *a], check=True, capture_output=True)
            git("init", "-q")
            git("add", ".")
            git("commit", "-q", "-m", "base")
            (root / "web/src/a.ts").write_text("1\n")  # worktree edit, uncommitted
            (root / "web/src/new.ts").write_text("n\nn\n")  # untracked file counts
            old, code_size.ROOT = code_size.ROOT, root
            try:
                out = io.StringIO()
                with redirect_stdout(out):
                    self.assertEqual(code_size.main(["--json", "--base", "HEAD"]), 0)
            finally:
                code_size.ROOT = old
            report = json.loads(out.getvalue())
            self.assertEqual(report["base"]["clients"]["web"]["product"], 3)  # last line without newline counts
            self.assertEqual(report["base"]["clients"]["web"]["test"], 1)
            self.assertEqual(report["head"]["clients"]["web"]["product"], 3)  # 1 + 2 untracked
            self.assertEqual(report["delta"]["clients_total"], {"product": 0, "test": 0})
            self.assertEqual(report["base"]["clients_total"]["product"], 5)


if __name__ == "__main__":
    unittest.main()
