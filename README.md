# Passage

**You shall pass.**

A local SOCKS5 proxy for Android. Messaging apps on the phone that support a
SOCKS5 proxy connect to it on `127.0.0.1`, and Passage picks the best
available connection for them automatically.

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

### Proxy access

The proxy listens on `127.0.0.1` with a random port, username and password
made on the phone. Settings can turn the password off for apps that can't
send one, or make a new port and password.

## Build

See [AGENTS.md](AGENTS.md).

## License

[MIT](LICENSE)
