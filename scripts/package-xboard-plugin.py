#!/usr/bin/env python3
"""Build/check the official ZIP and updater manifest together (no third party deps)."""
import argparse
import hashlib
import io
import json
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile, ZipInfo

root = Path(__file__).resolve().parent.parent
source = root / "integrations/xboard/AriNode"
config = json.loads((source / "config.json").read_text(encoding="utf-8"))
buffer = io.BytesIO()
with ZipFile(buffer, "w", ZIP_DEFLATED) as archive:
    for path in sorted(source.rglob("*")):
        if path.is_file():
            item = ZipInfo("AriNode/" + path.relative_to(source).as_posix(), (2020, 1, 1, 0, 0, 0))
            item.create_system = 3
            item.external_attr = 0o100644 << 16
            item.compress_type = ZIP_DEFLATED
            archive.writestr(item, path.read_text(encoding="utf-8").encode())
payload = buffer.getvalue()
manifest = json.dumps({"code": config["code"], "version": config["version"],
                       "sha256": hashlib.sha256(payload).hexdigest(), "size": len(payload)}, indent=2) + "\n"
outputs = {root / "dist/arinode-xboard-plugin.zip": payload,
           root / "dist/xboard-plugin-update.json": manifest.encode()}
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--check", action="store_true")
args = parser.parse_args()
for path, content in outputs.items():
    if args.check:
        if not path.exists() or path.read_bytes() != content:
            raise SystemExit(f"Stale plugin package/manifest: {path.name}; run scripts/package-xboard-plugin.py")
    else:
        path.write_bytes(content)
print(f"AriNode XBoard plugin {config['version']}: ZIP and SHA-256 manifest {'verified' if args.check else 'built'}")
