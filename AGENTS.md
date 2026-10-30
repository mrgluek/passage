# Passage — agent guide

Passage is an Android app that runs a local SOCKS5 proxy on `127.0.0.1` for
other apps on the phone. It has two parts:

- `core/`: the Go router (module `github.com/mrgluek/passage/core`).
  `tunnel/` is the router and SOCKS5 server, `cmd/libpassage` exports it as
  JNI methods of `info.gluek.passage.Native`, and `cmd/passage-cli` runs it
  standalone on Linux or macOS.
- `android/`: the Kotlin app (`info.gluek.passage`), a foreground service
  around that library, a home screen (state, proxy link, status) with
  start/stop, report and settings in the title bar, and a settings screen.
  UI strings live in `res/values/strings.xml` with a Russian translation in
  `values-ru/`.

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

Go router (from `core/`; `cmd/libpassage` builds for Android only):

```bash
go vet ./tunnel ./cmd/passage-cli
go test -race ./...
```

Shared libraries for `arm64-v8a` and `armeabi-v7a`, into
`android/app/src/main/nativeLibs/`:

```bash
scripts/build-native.sh
```

Android app (from `android/`):

```bash
./gradlew --no-daemon assembleRelease
```

This builds one APK per ABI plus a universal one; `versionCode` becomes
`versionCode * 10 + ABI digit` (universal 0, armeabi-v7a 1, arm64-v8a 2).
Without signing variables the release APKs are unsigned, so they can't be
installed by mistake: a debug-signed build could not be updated by real
releases.
`release.yml` signs tagged builds with `PASSAGE_KEYSTORE` (PKCS12),
`PASSAGE_KEYSTORE_PASS` and `PASSAGE_KEY_ALIAS`.

CI (`ci.yml`) runs the same steps on every push and pull request, except
those that only touch the website, store listing or docs.

## Release

1. Bump `versionCode` and `versionName` in `android/app/build.gradle.kts`.
2. Add a `## <version>` section to `CHANGELOG.md`: short, user-facing.
3. Tag `v<version>` on `main` and push the tag; `release.yml` builds, signs
   and publishes the APK with `SHA256SUMS`, then deploys the website.

## Website and F-Droid repository

`scripts/build-site.sh` builds the website (`site/`) together with an
F-Droid repository of the release APKs, and `release.yml` deploys it to
GitHub Pages at `https://passage.gluek.info`. To publish website changes
between releases, run `site.yml` by hand: it rebuilds the site with the
latest release's APKs. The listing text and icon come
from `fastlane/metadata/android/`, the app metadata from `fdroid/metadata/`.
The repository index is signed with its own key, from the `release`
environment secrets `FDROID_KEYSTORE_B64` (PKCS12), `FDROID_KEYSTORE_PASS`
and `FDROID_KEY_ALIAS`; the optional `FDROID_MIRRORS` lists mirrors, one URL
per line, each ending in `/fdroid`.
