#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out="$root/ios/build"
clang=$(xcrun --find clang)
mkdir -p "$out"

build_archive() {
    local sdk=$1 target=$2 name=$3 sdk_path
    sdk_path=$(xcrun --sdk "$sdk" --show-sdk-path)
    mkdir -p "$out/$name/Headers"
    (
        cd "$root/core"
        CGO_ENABLED=1 GOOS=ios GOARCH=arm64 CC="$clang" \
            CGO_CFLAGS="-isysroot $sdk_path -target $target" \
            CGO_LDFLAGS="-isysroot $sdk_path -target $target" \
            go build -tags ios -buildmode=c-archive \
            -o "$out/$name/libPassageCore.a" ./cmd/libpassage-ios
    )
    cp "$out/$name/libPassageCore.h" "$out/$name/Headers/PassageCore.h"
    cat > "$out/$name/Headers/module.modulemap" <<'MODULE'
module PassageCore {
    header "PassageCore.h"
    export *
}
MODULE
}

build_archive iphoneos arm64-apple-ios16.0 ios-arm64
build_archive iphonesimulator arm64-apple-ios16.0-simulator ios-arm64-simulator

rm -rf "$out/Passage.xcframework"
xcodebuild -create-xcframework \
    -library "$out/ios-arm64/libPassageCore.a" -headers "$out/ios-arm64/Headers" \
    -library "$out/ios-arm64-simulator/libPassageCore.a" -headers "$out/ios-arm64-simulator/Headers" \
    -output "$out/Passage.xcframework"
