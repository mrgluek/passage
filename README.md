# Passage

**You shall pass.**

A local SOCKS5 proxy for messaging apps on Android, Windows, Linux and macOS.
Apps that support a SOCKS5 proxy connect to it on `127.0.0.1`, and Passage
picks the best available connection for them automatically. Android,
Windows 10+ and macOS 13+ have an app; Linux, macOS and
Windows can run it as `passage-cli` ([On a computer](#on-a-computer)).

Website: <https://passage.gluek.info>

## Use

1. Install the APK for your phone from
   [Releases](https://github.com/mrgluek/passage/releases) (`arm64` for most
   phones, `armv7` for older 32-bit ones, `universal` if unsure) and check it
   against `SHA256SUMS`.
2. Tap the start button. Allow notifications and running in the background.
   If you received a link (`https://passage.gluek.info/link#…` or
   `passage://…`), open it or paste it in Settings: it adds a tunnel for
   networks where nothing else gets through. Passage works without one too.
3. Tap **Copy proxy link** and paste it into the proxy settings of the other
   app, or tap **Open proxy link** if the app adds proxies from links.

If something doesn't connect, **Test network** in the menu shows what works on
the current network; **Share** sends the result.

### Verify the APK

The package name and the SHA-256 of the signing certificate:

```
info.gluek.passage
C3:C9:73:50:37:FF:A5:00:6D:D9:88:AB:29:D6:11:D6:A4:41:31:E5:3F:D8:1D:FA:CF:06:49:4C:33:61:41:FA
```

- With [AppVerifier](https://github.com/soupslurpr/AppVerifier): copy the two
  lines above, then in AppVerifier pick Passage and tap
  **Verify from clipboard**.
- With `apksigner`: `apksigner verify --print-certs passage-<version>-arm64.apk`
  shows the same SHA-256.
- With the GitHub CLI, against the release's attestation:
  `gh release verify-asset v<version> passage-<version>-arm64.apk -R mrgluek/passage`.

Passage shows the same fingerprint in Settings → About.

### Updates in the app

Settings → **Updates** checks for a new version, downloads it and asks Android
to install it. With the proxy on, the app also checks once a day and shows a
notification; this can be turned off on the same screen.

### Updates with F-Droid

Passage has its own F-Droid repository. Open
<https://passage.gluek.info> on the phone and tap **Add repository**, or
scan the QR code there with F-Droid, Droid-ify or Neo Store. The page also
shows the repository address and fingerprint for adding it by hand.

### Updates with Obtainium

[Obtainium](https://github.com/ImranR98/Obtainium) installs Passage from
the GitHub releases and keeps it up to date:

1. In Obtainium, tap **Add app** and enter
   `https://github.com/mrgluek/passage`, or open
   `obtainium://add/https://github.com/mrgluek/passage` on the phone.
2. Under **Filter APKs by regular expression**, enter `arm64` (or `armv7`
   for a 32-bit phone), so it always picks the APK for your phone.
3. Tap **Add**, then **Install**.

Android only installs an update signed with the same key as the installed
app, so a tampered APK can't replace Passage.

### My route

Settings → **My route** takes the address of your own SOCKS5 proxy: for
example `127.0.0.1:9050` or `user:password@host:port`, or a pasted
`socks5://…` or `t.me/socks` link. Passage tries it first when it needs a
route, then the link's routes, and it can also be chosen as the only route.
`passage-cli -my <address>` does the same.

### Proxy access

The proxy listens on `127.0.0.1` with a random port, username and password
made on the phone. Settings can turn the password off for apps that can't
send one, or make a new port and password.

## On a computer

On Windows 10 or later, unpack `passage-<version>-windows-amd64.zip` and run
`passage.exe`; there is nothing else to install. It shows a window like the
phone app's and keeps running in the notification area when the window is
closed. Everything stays in the unpacked folder: settings in `passage.ini`,
the link in `link.txt` (one link per line) and the app history in
`passage.log`. Put a new `link.txt` there to update the link; Passage reads
it again within a few seconds. The window uses the Microsoft Edge WebView2
Runtime that comes with Windows; on a system without it, Passage offers the
download and the proxy keeps working. SmartScreen may warn about an unsigned
program first.

Settings → **Updates** checks for a new version and installs it: Passage
replaces its program files in the same folder and starts again, keeping
`passage.ini`, `link.txt` and the app history. If the new version does not
start, the previous one comes back.

On macOS 13 or later, unpack `passage-<version>-darwin-arm64.zip` for Apple
silicon or `passage-<version>-darwin-amd64.zip` for Intel Macs.
Move `Passage.app` to Applications before enabling **Start Passage when I
sign in** in its settings. It starts in the menu bar at login and restores
the saved proxy state. Opening Passage again shows its existing window.
The website's **Open in Passage** button opens received `passage://` links
in the app. Passage asks for confirmation before importing them.
After replacing a build, quit the old copy and open the installed app once
to register its link handler.

The macOS app checks for updates once a day while the proxy runs. Turn this
off in Settings → Updates, or check from the main window. Downloads start
only when you choose Download; Install and restart keeps your settings.

**The macOS app is not signed with an Apple Developer ID and is not
notarized.** macOS may block it on first launch. After trying to open it,
use **System Settings → Privacy & Security → Open Anyway**
([Apple's instructions](https://support.apple.com/en-gb/102445)). Alternatively,
remove quarantine from this app in Terminal, then open it:

```bash
xattr -dr com.apple.quarantine /Applications/Passage.app
open /Applications/Passage.app
```

Adjust the path if you installed it elsewhere. This removes the downloaded
app's quarantine flag; it does not disable Gatekeeper for other apps.

Releases also have `passage-cli` for Linux and macOS (`amd64`, `arm64`) and
Windows (`amd64`): the same proxy without the app. Unpack the archive for
your system and run

```
passage-cli -link-file link.txt
```

with a link saved in `link.txt` (or no flags to run without one), then set
the SOCKS5 proxy `127.0.0.1:18000` in the other app. `passage-cli -h` lists
the options. Keep `passage-iroh` next to `passage-cli`: some links need it.
On macOS, allow the unsigned binaries once with
`xattr -dr com.apple.quarantine <unpacked folder>`. On Windows,
`passage-cli.exe` is in the same archive as `passage.exe`; run it from a
terminal in the unpacked folder.

## Build

See [AGENTS.md](AGENTS.md).

Build the macOS menu bar app with Go, Rust and Xcode command line tools:

```bash
scripts/build-mac.sh dev build/mac
ditto -x -k build/mac/passage-dev-darwin-arm64.zip build/mac
open build/mac/Passage.app
```

Use `amd64` as the script's third argument on Intel Macs and in the archive
name. The bundle includes the iroh helper and uses the same window as the
Windows app. Closing the window keeps Passage running in the menu bar;
choose Open Passage to show it again, or Quit to stop it. Settings and
`link.txt` live in `~/Library/Application Support/Passage`. The executable
also accepts `-dir <folder>` for a separate test configuration.

Builds use an ad hoc signature, without an Apple Developer ID or notarization.
See the first-launch instructions under [On a computer](#on-a-computer).

The native build also needs Python 3.12+ and Rust 1.99.1 with rustup.
It builds the pinned iroh helper for both Android ABIs from source.
Set `PASSAGE_LINK_AUTHOR_KEY` to the author’s raw Ed25519 public key
(base64url without padding) to recognize signed v2 links. CI and releases
read it from the repository variable of the same name. Without it,
imports require the unverified-author confirmation.

V2 links add routes or replace the saved set after confirmation. Connections
try routes in priority order. Importing while running restarts the proxy.
The route menu shows each route's priority, ID and type. Selecting one uses
only that route; `passage-cli` accepts `-mode route:<id>` too.
The shared link codec is copied from `passage-link` with
`scripts/sync-link-codec.sh`; builds do not need that checkout.

Experimental HTTP routes use streaming HTTPS GET requests and can be selected
with `-mode http`. They require a matching `passage-http-exit` origin and a v2 link.

## License

[MIT](LICENSE)
