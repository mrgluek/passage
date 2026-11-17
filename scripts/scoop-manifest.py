#!/usr/bin/env python3
"""Writes the Scoop manifest for one release.

Usage: scoop-manifest.py <version> <windows-amd64.zip> <zip-url> <out.json>
"""
import hashlib
import json
import sys

version, archive, url, out = sys.argv[1:5]
with open(archive, "rb") as f:
    digest = hashlib.sha256(f.read()).hexdigest()
manifest = {
    "version": version,
    "description": "Local SOCKS5 proxy for messaging apps that picks the best available connection",
    "homepage": "https://passage.gluek.info",
    "license": "MIT",
    "architecture": {
        "64bit": {
            "url": url,
            "hash": digest,
            "extract_dir": f"passage-{version}-windows-amd64",
        }
    },
    "bin": "passage-cli.exe",
    "shortcuts": [["passage.exe", "Passage"]],
    "notes": [
        "Passage keeps its settings in Scoop's persist folder.",
        "To update, quit Passage from its notification area icon, then run: scoop update passage",
    ],
}
with open(out, "w", encoding="utf-8") as f:
    json.dump(manifest, f, indent=4)
    f.write("\n")
