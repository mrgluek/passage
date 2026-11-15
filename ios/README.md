# iOS prototype

Requires Xcode, Go, and XcodeGen 2.46.0. If `ios/build/Passage.xcframework` is missing, run `bash ios/build-core.sh` from the repository root before generating the project.
Run `cd ios && xcodegen generate`, then `xcodebuild -project Passage.xcodeproj -scheme Passage -destination 'platform=iOS Simulator,name=iPhone 17' build test`.
`project.yml` is the source of truth; the generated project and `build/` are ignored. The app uses Swift 6, targets iPhone on iOS 16+, and has signing disabled for simulator builds. Set signing locally for a device build.

The proxy starts only on user action; this prototype is foreground only.
On activation it calls `passage_recheck`; network changes call `passage_networkChanged`, and status polling runs only while active.
On background it does nothing special; no background modes are enabled.
