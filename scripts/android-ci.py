#!/usr/bin/env python3
"""Restore APK signing and Firebase client configuration without logging secrets."""
import base64
import json
import os
from pathlib import Path

config = Path.home() / ".config/remote-agent"
config.mkdir(parents=True, exist_ok=True, mode=0o700)
try:
    signing = json.loads(os.environ["ANDROID_SIGNING"])
    key = base64.b64decode(signing["keystoreBase64"], validate=True)
    values = {"storeFile": "release.jks", **{k: signing[k] for k in ("storePassword", "keyAlias", "keyPassword")}}
    # Values are generated as simple ASCII. Reject Java-properties delimiters
    # rather than silently producing a different password or path.
    if any(not isinstance(v, str) or not v or any(c in v for c in "\r\n\\:= ") for v in values.values()):
        raise ValueError()
    firebase = json.loads(os.environ["ANDROID_GOOGLE_SERVICES"])
    for name, content in {
        "release.jks": key,
        "signing.properties": "".join(f"{k}={v}\n" for k, v in values.items()).encode(),
        "google-services.json": json.dumps(firebase).encode(),
    }.items():
        target = config / name
        target.write_bytes(content)
        target.chmod(0o600)
except (KeyError, ValueError, TypeError):
    raise SystemExit("Missing or invalid Android signing/Firebase secrets") from None
