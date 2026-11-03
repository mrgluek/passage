#!/usr/bin/env python3
"""Build the pinned iroh helper with explicit relay selection.

No arguments: both Android ABIs, 16-KB aligned, into the Android project.
`desktop <rust-target> <out-file>`: one Linux or macOS binary for passage-cli.
"""
import hashlib
import os
from pathlib import Path
import platform
import re
import shutil
import struct
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
REV = "fa449072003cb707b40e8445425e3306fd2f5d58"
DIGEST = "eaa2d49c0844b22d2ca582d44c8fe91e2155b8483eaa5a5f9bf4e9f7e3844d74"
TARGETS = {"arm64-v8a": ("aarch64-linux-android", "aarch64-linux-android"),
           "armeabi-v7a": ("armv7-linux-androideabi", "armv7a-linux-androideabi")}

def aligned(path):
    if not path.exists(): return False
    data = path.read_bytes()
    if data[:4] != b"\x7fELF" or data[5] != 1: return False
    wide = data[4] == 2
    offset = struct.unpack_from("<Q" if wide else "<I", data, 32 if wide else 28)[0]
    size, count = struct.unpack_from("<HH", data, 54 if wide else 42)
    loads = []
    for i in range(count):
        at = offset + i * size
        if struct.unpack_from("<I", data, at)[0] == 1:
            loads.append(struct.unpack_from("<Q" if wide else "<I", data, at + (48 if wide else 28))[0])
    return bool(loads) and min(loads) >= 16384

def prepare(work):
    archive = work / "source.tar.gz"
    with urllib.request.urlopen("https://codeload.github.com/weavedwires/dumbpipe-over-chatmail/tar.gz/" + REV, timeout=60) as response:
        archive.write_bytes(response.read(4 * 1024 * 1024))
    if hashlib.sha256(archive.read_bytes()).hexdigest() != DIGEST:
        raise SystemExit("helper source checksum mismatch")
    with tarfile.open(archive) as source: source.extractall(work, filter="data")
    source = work / ("dumbpipe-over-chatmail-" + REV)
    manifest = source / "Cargo.toml"
    manifest.write_text(manifest.read_text().replace('iroh = { version = "0.35", default-features = false }', 'iroh = { version = "0.35", default-features = false, features = ["test-utils"] }'))
    main = source / "src/main.rs"
    text = re.sub(r'const DEFAULT_RELAYS: &\[&str\] = &\[.*?\];', 'const DEFAULT_RELAYS: &[&str] = &[];', main.read_text(), flags=re.S)
    text = text.replace('builder = builder.relay_mode(RelayMode::Custom(DEFAULT_RELAY_MAP.clone()));', 'anyhow::bail!("an explicit relay is required");')
    text = text.replace('let endpoint = builder.bind().await.context("failed to bind endpoint")?;', 'if common.no_direct { builder = builder.path_selection(iroh::endpoint::PathSelection::RelayOnly); }\n    let endpoint = builder.bind().await.context("failed to bind endpoint")?;')
    main.write_text(text)
    crate = work / "iroh.crate"
    with urllib.request.urlopen("https://static.crates.io/crates/iroh/iroh-0.35.0.crate", timeout=60) as response:
        crate.write_bytes(response.read(4 * 1024 * 1024))
    if hashlib.sha256(crate.read_bytes()).hexdigest() != "6ca758f4ce39ae3f07de922be6c73de6a48a07f39554e78b5745585652ce38f5":
        raise SystemExit("iroh source checksum mismatch")
    vendor = source / "vendor"
    vendor.mkdir()
    with tarfile.open(crate) as archive: archive.extractall(vendor, filter="data")
    utilities = vendor / "iroh-0.35.0/src/test_utils.rs"
    utilities.write_text(utilities.read_text().replace("let config = ServerConfig {", "let config = ServerConfig {\n        metrics_addr: None,", 1))
    # Relay only: no port mapping requests to the local router.
    magicsock = vendor / "iroh-0.35.0/src/magicsock.rs"
    old = "portmapper::Client::with_metrics(Default::default(), metrics)"
    if old not in magicsock.read_text(): raise SystemExit("iroh port mapper not found")
    magicsock.write_text(magicsock.read_text().replace(old, "portmapper::Client::with_metrics(portmapper::Config { enable_upnp: false, enable_pcp: false, enable_nat_pmp: false }, metrics)"))
    with manifest.open("a") as manifest_file:
        manifest_file.write('\n[patch.crates-io]\niroh = { path = "vendor/iroh-0.35.0" }\n')
    shutil.copyfile(ROOT / "scripts/iroh.Cargo.lock", source / "Cargo.lock")
    return source

def android():
    outputs = {abi: ROOT / "android/app/src/main/nativeLibs" / abi / "libpassage_iroh.so" for abi in TARGETS}
    stamp = ROOT / "android/app/src/main/nativeLibs/.iroh-build"
    expected = REV + " relay-only-v2 16384"
    if stamp.exists() and stamp.read_text().strip() == expected and all(aligned(p) for p in outputs.values()):
        return
    ndk = Path(os.environ["ANDROID_HOME"]) / "ndk/27.2.12479018/toolchains/llvm/prebuilt" / ("darwin-x86_64" if platform.system() == "Darwin" else "linux-x86_64")
    with tempfile.TemporaryDirectory(prefix="passage-iroh-build-") as temporary:
        source = prepare(Path(temporary))
        for abi, (target, clang) in TARGETS.items():
            env = dict(os.environ)
            env.setdefault("CARGO_TARGET_DIR", str(ROOT / "android/build/iroh-target"))
            name = target.upper().replace("-", "_")
            compiler = str(ndk / "bin" / (clang + "24-clang"))
            env["CARGO_TARGET_" + name + "_LINKER"] = compiler
            env["CC_" + target.replace("-", "_")] = compiler
            env["AR_" + target.replace("-", "_")] = str(ndk / "bin/llvm-ar")
            flags = "-C link-arg=-Wl,-z,max-page-size=16384"
            if env.get("PASSAGE_RUST_SYSROOT"):
                flags += " --sysroot=" + env["PASSAGE_RUST_SYSROOT"]
            else:
                subprocess.run(["rustup", "target", "add", target], check=True)
            env["CARGO_TARGET_" + name + "_RUSTFLAGS"] = flags
            subprocess.run(["cargo", "build", "--locked", "--release", "--target", target], cwd=source, env=env, check=True)
            binary = Path(env["CARGO_TARGET_DIR"]) / target / "release/dumbpipe"
            if not aligned(binary): raise SystemExit("helper does not have 16-KB alignment")
            outputs[abi].parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(binary, outputs[abi]); outputs[abi].chmod(0o755)
    stamp.write_text(expected + "\n")

def desktop(target, output):
    with tempfile.TemporaryDirectory(prefix="passage-iroh-build-") as temporary:
        source = prepare(Path(temporary))
        env = dict(os.environ)
        env.setdefault("CARGO_TARGET_DIR", str(ROOT / "core/build/iroh-target"))
        if target.endswith("-musl"):
            env.setdefault("CC_" + target.replace("-", "_"), "musl-gcc")
        if shutil.which("rustup"):
            subprocess.run(["rustup", "target", "add", target], check=True)
        subprocess.run(["cargo", "build", "--locked", "--release", "--target", target], cwd=source, env=env, check=True)
        output = Path(output)
        output.parent.mkdir(parents=True, exist_ok=True)
        binary = "dumbpipe.exe" if "windows" in target else "dumbpipe"
        shutil.copyfile(Path(env["CARGO_TARGET_DIR"]) / target / "release" / binary, output)
        output.chmod(0o755)

if len(sys.argv) == 4 and sys.argv[1] == "desktop":
    desktop(sys.argv[2], sys.argv[3])
elif len(sys.argv) == 1:
    android()
else:
    raise SystemExit("usage: prepare-iroh.py [desktop <rust-target> <out-file>]")
