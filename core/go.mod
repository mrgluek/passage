module github.com/mrgluek/passage/core

go 1.25.0

require (
	fyne.io/systray v1.12.2
	github.com/coder/websocket v1.8.15
	github.com/getlantern/soar v0.2.4
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
)

require (
	github.com/fxamacker/cbor/v2 v2.10.0 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/x448/float16 v0.8.4 // indirect
)

require (
	github.com/mrgluek/passage-link v0.0.0
	golang.getoutline.org/sdk v0.0.23
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sys v0.47.0
)

replace github.com/mrgluek/passage-link => ./third_party/passage-link
