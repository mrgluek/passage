Passage for macOS

Requires macOS 13 or later. Move Passage.app to Applications before
enabling "Start Passage when I sign in" in its settings. At login it
starts in the menu bar and restores the saved proxy state. Opening
Passage again shows the existing window. Choose Quit to stop it.
The website's "Open in Passage" button opens passage:// links in the app.
Passage asks for confirmation before importing them.
After replacing a build, quit the old copy and open the installed app once
to register its link handler.

This build has an ad hoc signature. It is not signed with an Apple
Developer ID and is not notarized. macOS may block the first launch.
After trying to open it, go to System Settings > Privacy & Security
and choose Open Anyway. Alternatively, run these commands in Terminal:

  xattr -dr com.apple.quarantine /Applications/Passage.app
  open /Applications/Passage.app

Adjust the path if you installed it elsewhere. The command removes
quarantine from Passage only; it does not disable Gatekeeper.

Settings, link.txt and logs are in:
  ~/Library/Application Support/Passage

If macOS asks for approval to launch at login, allow Passage in
System Settings > General > Login Items, then enable it in Passage again.
The setting reflects macOS's current status. If the app is moved or
replaced, check the setting again in the installed copy.
