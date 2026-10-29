# Passage — agent guide

Passage is an Android app that runs a local SOCKS5 proxy on `127.0.0.1` for
other apps on the phone. It has two parts:

- a Go library (the router), built as a shared library for Android;
- a Kotlin Android app: a foreground service around that library, plus the
  settings and status screens.

> Draft. The source tree is not in the repo yet; update the paths below when
> it lands.

## Rules

- English only: replies, code, comments, commit messages, docs.
- Every text file is UTF-8 without BOM.
- This is a public repository. Keep code comments short and limited to what
  the code needs. Keep commit messages, PR descriptions, issues and release
  notes short and user-facing.
- Never commit server names, IP addresses, keys, links with credentials, or
  hosting-provider names. Use placeholders such as `example.org`.
- Do not push, tag or open pull requests unless the owner asks.

## Requirements

- JDK 17
- Android SDK with platform 36, build-tools 35.0.0 and NDK 27.2.12479018
- Go (version from `go.mod`)

## Build and test

Go library (from the Go module directory):

```bash
go vet ./...
go test ./...
```

Shared library for arm64 Android:

```bash
NDK="$ANDROID_HOME/ndk/27.2.12479018/toolchains/llvm/prebuilt/<host>"
CGO_ENABLED=1 GOOS=android GOARCH=arm64 \
  CC="$NDK/bin/aarch64-linux-android24-clang" \
  go build -trimpath -buildmode=c-shared \
  -ldflags="-s -w -extldflags=-Wl,-z,max-page-size=16384" \
  -o <app>/src/main/jniLibs/arm64-v8a/<lib>.so ./cmd/<lib>
```

The 16 KB page alignment is required for native libraries on Android 15+.

Android app (from the Gradle project directory):

```bash
./gradlew --no-daemon assembleRelease
```

CI runs the same steps in GitHub Actions.
