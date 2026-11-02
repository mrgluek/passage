# Changelog

## 0.3.2

- Fixes automatic routing when only the tunnel is reachable, including
  stalled Telegram connections and incorrect direct/offline status.
- Shows Telegram's route and connection checks when no mail app is connected.

## 0.3.1

- Adds an option to let the CPU sleep while Passage runs; keeping it awake
  remains enabled by default for reliable background connections.
- Fixes a crash when rotating the screen during a network test and keeps
  the test result across rotation.
- Saves startup errors, crashes and restart history for shared reports.
- Checks the background service less often while it is working; network
  changes still trigger a check.
- Fixes connections failing while a tunnel is still starting.
- Stops retrying refused destinations through iroh and keeps other
  destinations on their preferred connection.
- Limits local connections and closes pending connections when stopped.

## 0.3.0

- One link can carry several connections. Passage tries them in order and
  moves to the next when one doesn't get through.
- Links signed by the author are marked as verified; others ask before
  import.
- The status shows which connection mail and Telegram use, and the state of
  each one.
- Test network checks each connection separately.
- Simpler settings: probe hosts and the query-length option are gone.
- Links from earlier versions keep working.

## 0.2.1

- Improves automatic connection checks.
- Handles IPv4-only networks more reliably.
- Tunnel links can specify server IPv6 support.
- Starts the tunnel only when needed and stops it when unused.
- Smaller APKs.

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
