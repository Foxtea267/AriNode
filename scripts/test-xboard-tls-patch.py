#!/usr/bin/env python3
"""Apply/test the companion patch against the pinned XBoard image source."""
import argparse
from pathlib import Path
import subprocess
import tempfile
from urllib.request import Request, urlopen

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--source", type=Path, help="Use an existing unmodified 4f48e61 source tree")
parser.add_argument("--php", default="php")
parser.add_argument("--php-arg", action="append", default=[])
args = parser.parse_args()
repo = Path(__file__).resolve().parent.parent
paths = ["app/Protocols/General.php", "app/Services/ServerService.php", "app/Http/Requests/Admin/ServerSave.php"]
commit = "4f48e61a2cbc6db5338872b6bdb45ef954ec1256"
with tempfile.TemporaryDirectory(prefix="arinode-xboard-tls-") as temporary:
    target = Path(temporary)
    for name in paths:
        if args.source:
            content = (args.source / name).read_bytes()
        else:
            request = Request(f"https://raw.githubusercontent.com/cedar2025/Xboard/{commit}/{name}", headers={"User-Agent": "AriNode-tests"})
            with urlopen(request, timeout=30) as response:
                content = response.read()
        dest = target / name
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(content)
    patch = repo / "integrations/xboard/patches/4f48e61-tls-certificates.patch"
    subprocess.run(["git", "apply", "--check", str(patch)], cwd=target, check=True)
    subprocess.run(["git", "apply", str(patch)], cwd=target, check=True)
    helper = target / "app/Support/NodeTlsConfig.php"
    assert helper.read_text(encoding="utf-8") == (repo / "integrations/xboard/patches/NodeTlsConfig.php").read_text(encoding="utf-8")
    for name in [*paths, "app/Support/NodeTlsConfig.php"]:
        subprocess.run([args.php, *args.php_arg, "-l", str(target / name)], check=True)
    subprocess.run([args.php, *args.php_arg, str(repo / "integrations/xboard/tests/certificates.php"), str(target)], check=True)
