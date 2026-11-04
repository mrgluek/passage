#!/usr/bin/env python3
"""Checks a signed desktop update and restart in an isolated macOS app."""
import configparser
import functools
import http.server
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import threading
import time

ROOT = Path(__file__).resolve().parent.parent


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def main():
    if os.uname().sysname != "Darwin":
        raise SystemExit("run on macOS")
    with tempfile.TemporaryDirectory(prefix="passage-update-") as temporary, tempfile.TemporaryDirectory(prefix=".mac-update-", dir=ROOT / "core") as source:
        work, source = Path(temporary), Path(source)
        fixture = ROOT / "core/cmd/passage/testdata"
        shutil.copytree(ROOT / "core/cmd/passage", source, dirs_exist_ok=True)
        html = source / "ui/index.html"
        html.write_text(html.read_text().replace("</body>", "<script>" + (fixture / "mac-update.js").read_text() + "</script></body>"))
        native = source / "mac_darwin.m"
        native.write_text(native.read_text() + (fixture / "mac-update.m").read_text())
        dist = work / "dist"
        dist.mkdir()
        class Handler(http.server.SimpleHTTPRequestHandler):
            def copyfile(self, source, output):
                try:
                    super().copyfile(source, output)
                except (BrokenPipeError, ConnectionResetError):
                    pass

            def log_message(self, *_):
                pass
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), functools.partial(Handler, directory=str(dist)))
        threading.Thread(target=server.serve_forever, daemon=True).start()
        env = dict(os.environ)
        keys = subprocess.check_output(["go", "run", "./cmd/passage-update-manifest", "keygen"], cwd=ROOT / "core", text=True)
        env.update(line.split("=", 1) for line in keys.splitlines())
        env["PASSAGE_UPDATE_SOURCES"] = f"http://127.0.0.1:{server.server_port}"
        env["PASSAGE_UPDATE_TEST_RESULT"] = str(work / "result")
        try:
            run(str(ROOT / "scripts/build-mac.sh"), "9.0.1", str(dist), env=env)
            archive = next(dist.glob("*.zip"))
            old = work / "old"
            old.mkdir()
            run("ditto", "-x", "-k", str(archive), str(old))
            new = work / "new"
            shutil.copytree(old, new)
            target = subprocess.check_output(["go", "env", "GOARCH"], text=True).strip()
            flags = f"-X github.com/mrgluek/passage/core/update.PublicKey={env['PASSAGE_UPDATE_PUBLIC_KEY']} -X github.com/mrgluek/passage/core/update.ExtraSources={env['PASSAGE_UPDATE_SOURCES']}"
            for folder, version in ((old, "9.0.1"), (new, "9.0.2")):
                app = folder / "Passage.app"
                run("go", "build", "-ldflags", flags + f" -X main.version={version}", "-o", str(app / "Contents/MacOS/passage"), "./" + source.name, cwd=ROOT / "core", env=env)
                for key in ("CFBundleVersion", "CFBundleShortVersionString"):
                    run("/usr/libexec/PlistBuddy", "-c", f"Set :{key} {version}", str(app / "Contents/Info.plist"))
                run("codesign", "--force", "--sign", "-", str(app))
            name = f"passage-9.0.2-darwin-{target}.zip"
            run("ditto", "-c", "-k", str(new), str(dist / name))
            for suffix in ("arm64.apk", "armv7.apk", "windows-amd64.zip", f"darwin-{'amd64' if target == 'arm64' else 'arm64'}.zip"):
                (dist / f"passage-9.0.2-{suffix}").write_bytes(b"fixture")
            run("go", "run", "./cmd/passage-update-manifest", "sign", "9.0.2", str(dist), cwd=ROOT / "core", env=env)
            settings = work / "settings"
            settings.mkdir()
            ini = settings / "passage.ini"
            ini.write_text("[proxy]\nrunning=false\nauth=true\nport=24567\nuser=keep\npass=keep\nauto_update=false\n")
            result = work / "result"
            process = subprocess.Popen([str(old / "Passage.app/Contents/MacOS/passage"), "-dir", str(settings)], env=env, start_new_session=True)
            try:
                deadline = time.monotonic() + 180
                while not result.exists() and time.monotonic() < deadline:
                    time.sleep(0.1)
                assert result.exists(), "update did not restart"
                assert result.read_text() == "ok", result.read_text()
                assert process.wait(timeout=10) == 0, "old process failed"
                log = settings / "passage.log"
                while log.read_text().count("app quit") < 2 and time.monotonic() < deadline:
                    time.sleep(0.1)
                assert log.read_text().count("app start") == 2, "new process did not initialize"
                assert log.read_text().count("app quit") == 2, "new process did not quit"
                config = configparser.ConfigParser()
                config.read(ini)
                for key, value in {"port":"24567", "user":"keep", "pass":"keep", "auth":"true", "running":"false", "auto_update":"false"}.items():
                    assert config["proxy"][key] == value, f"changed {key}"
                run("codesign", "--verify", "--deep", "--strict", str(old / "Passage.app"))
                print(f"Production macOS archive: {archive.stat().st_size / 1048576:.1f} MiB")
                print(f"macOS update passed: signed check, {int((dist / name).stat().st_size / 1048576)} MiB download, install, restart, settings and lock release")
            finally:
                try:
                    os.killpg(process.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                process.wait(timeout=10)
        finally:
            server.shutdown()
            server.server_close()
            for key in ("PASSAGE_UPDATE_KEY", "PASSAGE_UPDATE_PUBLIC_KEY"):
                env.pop(key, None)
            cache = Path.home() / "Library/Caches/Passage/updates"
            if (dist / "update.json").exists():
                import hashlib
                digest = hashlib.sha256((dist / name).read_bytes()).hexdigest()
                for suffix in (".zip", ".zip.part"):
                    for download in cache.glob("*/" + digest + suffix):
                        download.unlink(missing_ok=True)


if __name__ == "__main__":
    main()
