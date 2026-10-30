# Changelog

## 0.2.0

- More ways to connect: when a direct connection doesn't get through,
  Passage tries other ways before the tunnel, and remembers per network
  what worked.
- Each mail server, and Telegram, gets its own route, so one server's
  trouble doesn't slow down the others.
- Works without a link: install it and start it; a link adds the tunnel.
- Links like `https://passage.gluek.info/link#…` open Passage and fill in
  the connection.
- **Test network** in the menu checks every server and every way at once;
  the result can be shared. **Check now** checks right away.
- The notification shows the current routes.
- After an update, the proxy turns itself back on if it was running.
- Explains why it needs notifications and running in the background.
- About lists the open-source licenses.

## 0.1.0

First public version.

- Local SOCKS5 proxy on `127.0.0.1` for other apps on the phone, with a
  random port, username and password; **Copy proxy link** sets up an app
  in one paste.
- Picks the connection automatically, or set it by hand: Auto, Always
  tunnel, Never tunnel.
- Set up with a `passage://` link: paste it, or tap it to open the app.
- Status screen, **Check now**, and a report you can send from the app.
- About shows the version and the signing certificate fingerprint.
- English and Russian.
- Android 7.0 and newer, 64-bit and 32-bit ARM phones.
