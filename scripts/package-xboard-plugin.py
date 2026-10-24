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
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--check", action="store_true")
args = parser.parse_args()
entries = {"AriNode/" + path.relative_to(source).as_posix(): path.read_text(encoding="utf-8").encode()
           for path in sorted(source.rglob("*")) if path.is_file()}
zip_path = root / "dist/arinode-xboard-plugin.zip"
manifest_path = root / "dist/xboard-plugin-update.json"
if args.check:
    # Deflate byte streams can differ between zlib/platform versions. Verify the
    # shipped archive's contents, then hash that archive, rather than recompressing.
    payload = zip_path.read_bytes()
    with ZipFile(io.BytesIO(payload)) as archive:
        if len(archive.namelist()) != len(entries) or set(archive.namelist()) != set(entries):
            raise SystemExit("Plugin ZIP contains missing, duplicate or extra files; rebuild it")
        for name, content in entries.items():
            if archive.read(name) != content:
                raise SystemExit(f"Stale plugin ZIP file: {name}; run scripts/package-xboard-plugin.py")
    expected = {"code": config["code"], "version": config["version"],
                "sha256": hashlib.sha256(payload).hexdigest(), "size": len(payload)}
    if json.loads(manifest_path.read_text(encoding="utf-8")) != expected:
        raise SystemExit("Stale plugin update manifest; run scripts/package-xboard-plugin.py")
    print(f"AriNode XBoard plugin {config['version']}: source, ZIP and SHA-256 manifest verified")
    raise SystemExit(0)

buffer = io.BytesIO()
with ZipFile(buffer, "w", ZIP_DEFLATED) as archive:
    for name, content in entries.items():
        item = ZipInfo(name, (2020, 1, 1, 0, 0, 0))
        item.create_system = 3
        item.external_attr = 0o100644 << 16
        item.compress_type = ZIP_DEFLATED
        archive.writestr(item, content)
payload = buffer.getvalue()
manifest = json.dumps({"code": config["code"], "version": config["version"],
                       "sha256": hashlib.sha256(payload).hexdigest(), "size": len(payload)}, indent=2) + "\n"
outputs = {zip_path: payload, manifest_path: manifest.encode()}
for path, content in outputs.items():
    path.write_bytes(content)
print(f"AriNode XBoard plugin {config['version']}: ZIP and SHA-256 manifest built")
