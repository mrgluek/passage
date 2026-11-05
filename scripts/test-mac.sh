#!/usr/bin/env bash
# Exercises the native window and HTML bridge in a separate test configuration.
# Requires a macOS desktop session. PASSAGE_SMOKE_IMAGE optionally saves a PNG.
set -euo pipefail

[[ $(uname -s) == Darwin ]] || { echo "run on macOS" >&2; exit 1; }
root="$(cd "$(dirname "$0")/.." && pwd)"
work=$(mktemp -d "$root/core/.mac-smoke.XXXXXX")
trap 'rm -rf "$work"' EXIT
cp -R "$root/core/cmd/passage/." "$work/"
cat "$work/testdata/mac-smoke.m" >> "$work/mac_darwin.m"
"${PYTHON:-python3}" - "$work" <<'PY'
from pathlib import Path
import sys

work = Path(sys.argv[1])
html = work / "ui/index.html"
script = (work / "testdata/mac-smoke.js").read_text()
html.write_text(html.read_text().replace("</body>", "<script>" + script + "</script></body>"))
for mode in ("menu", "system"):
    (work / mode).mkdir()
    (work / mode / "passage.ini").write_text("[proxy]\nrunning=false\nauth=false\n")
PY
cd "$root/core"
export PASSAGE_SMOKE_LINK
PASSAGE_SMOKE_LINK=$(go run ./cmd/passage/testdata/mac-smoke-link.go)
CGO_ENABLED=1 go build -race -o "$work/passage-smoke" "./${work##*/}"
for mode in menu system; do
"${PYTHON:-python3}" - "$work/passage-smoke" "$work/$mode" "$mode" <<'PY'
import configparser
import os
from pathlib import Path
import socket
import subprocess
import sys

folder = Path(sys.argv[2])
env = dict(os.environ, PASSAGE_SMOKE_TERMINATE=sys.argv[3])
subprocess.run([sys.argv[1], "-dir", str(folder)], env=env, check=True, timeout=30)
config = configparser.ConfigParser()
config.read(folder / "passage.ini")
with socket.socket() as sock:
    sock.settimeout(1)
    assert sock.connect_ex(("127.0.0.1", config.getint("proxy", "port"))) != 0, "proxy still listening after Quit"
events = (folder / "passage.log").read_text().splitlines()
assert "app quit" in events[-1], "missing shutdown event"
assert sum("app start" in line for line in events) == 1, "second process initialized the app"
print(f"{sys.argv[3]} shutdown passed: app quit logged and proxy listener closed")
PY
done
