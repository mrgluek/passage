module github.com/mrgluek/passage/core

go 1.25.0

require github.com/getlantern/soar v0.2.4

require (
	github.com/fxamacker/cbor/v2 v2.10.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
)

require (
	github.com/mrgluek/passage-link v0.0.0
	golang.getoutline.org/sdk v0.0.23
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/mrgluek/passage-link => ./third_party/passage-link
