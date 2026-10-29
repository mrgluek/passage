# Passage

**You shall pass.**

A local SOCKS5 proxy for Android. Other apps on the phone (Delta Chat,
ArcaneChat, etc.) connect to it on `127.0.0.1`, and Passage picks the best
available connection for them automatically.

## Use

1. Install the APK from [Releases](https://github.com/mrgluek/passage/releases)
   and check it against `SHA256SUMS`.
2. Open the `passage://` link you received, or paste it into the app, and
   tap **Start**. Allow notifications and running in the background.
3. In the other app, set the proxy to SOCKS5 `127.0.0.1`, port `42000`, no
   username or password. In Delta Chat: Settings → Advanced → Proxy →
   `socks5://127.0.0.1:42000`.

Any app on the phone can use the proxy port.

## Build

See [AGENTS.md](AGENTS.md).

## License

[MIT](LICENSE)
