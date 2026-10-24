#!/usr/bin/env python3
"""Verify TCP TLS hostname/chain and require a stable leaf fingerprint (no insecure mode)."""
import argparse
import hashlib
import json
import ssl
import socket
import time
from datetime import datetime, timezone

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--host", required=True)
parser.add_argument("--port", type=int, required=True)
parser.add_argument("--server-name", required=True, help="A hostname covered by the certificate SAN")
parser.add_argument("--duration", type=int, default=300)
parser.add_argument("--interval", type=int, default=30)
parser.add_argument("--ca-file", help="Optional trust anchor for local tests")
args = parser.parse_args()
if args.duration < 0 or args.interval < 1 or not 0 < args.port < 65536:
    parser.error("invalid duration, interval or port")
context = ssl.create_default_context(cafile=args.ca_file)
started = time.monotonic()
fingerprint = None
samples = args.duration // args.interval + 1
for sample in range(samples):
    if sample:
        time.sleep(max(0, started + sample * args.interval - time.monotonic()))
    with socket.create_connection((args.host, args.port), timeout=10) as sock:
        with context.wrap_socket(sock, server_hostname=args.server_name) as conn:
            der, info = conn.getpeercert(binary_form=True), conn.getpeercert()
    current = hashlib.sha256(der).hexdigest()
    print(json.dumps({"at": datetime.now(timezone.utc).isoformat(), "verified": True, "sha256": current,
                      "issuer": info.get("issuer"), "not_before": info.get("notBefore")}), flush=True)
    if fingerprint is not None and current != fingerprint:
        raise SystemExit("FAIL: certificate fingerprint changed during sampling")
    fingerprint = current
print(f"PASS: verified hostname/chain and stable SHA-256 across {samples} samples")
