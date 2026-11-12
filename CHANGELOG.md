# Changelog

## 0.8.1

- A route that stops answering now recovers by itself after an interruption,
  without stopping and starting the proxy.
- Clearer route status: a route you pinned by hand shows as pinned, the others
  as not used, and one that does not reply as not answering.
- The history notes when a route was restarted or skipped.

## 0.8.0

- A new home screen in four steps: add your link, turn the proxy on, add it to
  your chat app, and send a report if something is wrong. Tabs for Delta Chat,
  Velta, Telegram and other apps each show what to paste or tap.
- The status says in plain words whether Passage works, is connecting, or has a
  problem; the dot pulses while traffic flows. The notification says the same.
- A Details screen with routes, connections and the app log. The report is
  shared as one file, and it no longer includes your own network addresses.
- Language choice in Settings: System, English or Russian. Settings are grouped
  in cards, and the saved routes are shown as plain ticks until you edit them.
- Steadier route switching, also after the app restarts, and a subscription is
  now read from several sources at once.
- Subscription names and tg:// or velta:// links are handled more carefully.

## 0.7.2

- Steadier route switching: a slow connection that is simply idle no longer
  sends everything through another route. Each kind of traffic is judged on
  its own, and a route you pinned by hand stays in use.
- Faster start on a new network: Passage remembers which networks needed a
  tunnel and begins there, and a network change on the computer no longer
  restarts it for harmless adapter changes.
- Routes from a subscription link are loaded right after you add it, and the
  list is fetched faster when one address does not answer.
- Home shows a Start button when routes are loaded but the proxy is stopped.
- IPv6 addresses that the exit cannot reach no longer mark a working tunnel
  as unhealthy.

## 0.7.1

- On the computer, a subscription from the author now also takes over a route
  with the same name from an old link file at start, as it does on refresh.
- A gap between the Refresh now and Remove buttons.
- The "routes updated" notice is a bubble with a close button.
- Settings order: Route, My route, Saved routes, Link, Subscriptions, then
  the rest.

## 0.7.0

- A link can now refresh itself. A subscription link carries an address from
  which Passage loads the current list of routes, so routes can change without
  a new link. Your own edits (a route switched off, a changed priority) stay.
  Settings shows each subscription, with Refresh now and Remove, on Android,
  Windows and macOS. The owner can also close a subscription.
- Adds a new connection type for links (TURN).
- Passage notices a route that accepts a connection but never answers, uses
  another route, and tries the silent one last for a while.
- Fixes garbled arrows in the connect help on Android.

## 0.6.4

- Faster and steadier HTTP connection type: it reuses its connections.

## 0.6.3

- Faster uploads over the HTTP connection type, about eight times in our tests.
- HTTP routes in a link can set the TLS server name.
- Edit routes: turn a saved route off or change its priority, on Android,
  Windows and macOS. Edits stay on the device.
- Saved routes are listed the same way on every platform, and adding or
  replacing routes from a link works the same on a computer as on a phone.

## 0.6.2

- Android: a dark theme. Choose System, Light or Dark in Settings; by default
  the app follows the system.
- Android: unsaved text in Settings stays when the screen rotates or the theme
  changes.

## 0.6.1

- Passage can check for a new version and install it from inside the app, in
  Settings → Updates, on Android, Windows and macOS. The download continues
  where it stopped.
- Passage for macOS: a menu bar app for Apple silicon and Intel, with launch
  at login and connection-link imports.
- macOS builds have no Apple Developer ID signature or notarization.
  First-launch instructions are included in the ZIP.
- Fixes a crash on older Android versions when a link route starts its helper.
- Copy proxy link on Android now gives a socks5:// link, as on a computer. On
  a computer, the second link button also opens the link in an app that
  handles it.

## 0.6.0

- Adds a new connection type for links (HTTP).
- A shorter route bar lists individual link routes with their priorities.
- Passage for Windows: `passage.exe` shows a window like the phone app's and
  keeps running in the notification area. It is portable: settings and the
  link stay next to it, in `passage.ini` and `link.txt`.
- Auto remembers each network where it needed a server route and starts
  there at once the next time, instead of checking again first.
- Choosing another server route type now moves open connections to it at
  once.
- The network test shows how far each server route gets, step by step.
- `passage-cli -test` runs the network test once and prints it as JSON.

## 0.5.2

- When a server route cannot reach an address, Passage tries that route
  again for it after a few seconds instead of half a minute, so apps no
  longer stay on a slower route after a single failed connection.
- The app title and the `passage-cli` start line show the Passage version.

## 0.5.1

- On a computer, `passage-iroh` now listens only on the computer itself, so
  macOS and Windows no longer ask to allow incoming network connections.
- The website lists the downloads for Linux, macOS and Windows.

## 0.5.0

- My route: set the address of your own SOCKS5 proxy in Settings (for
  example 127.0.0.1:9050). It is tried first, then the link's routes, and
  can be chosen as the only route.
- `passage-cli` for Linux, macOS and Windows: the same proxy on a computer,
  with all connection types, in the release downloads.
- When a server refuses a connection, quick retries from apps are answered
  at once and no longer make Passage treat the connection as broken.

## 0.4.0

- Adds a new connection type for links (WSS).
- With several connections in a link, a backup starts after 2 seconds
  instead of waiting for a stuck connection to time out.
- The main screen lets you choose how Passage connects: automatically,
  through one of the link's connections, or directly.
- Proxy settings are shown as values you can tap to copy, with buttons to
  copy or open the proxy link.
- The status line is easier to read.
- A bug report button checks the network and shares a full report.
- Start and stop sit next to the proxy status.
- The CPU is kept awake only while a connection needs it; the separate
  setting is gone. Reports include battery readings.
- Fixes the proxy sometimes failing to restart for a minute after a stop.

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
