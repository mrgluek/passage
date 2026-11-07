package main

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"os"

	links "github.com/mrgluek/passage-link/link"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "invite" {
		pub, key, err := ed25519.GenerateKey(nil)
		if err != nil {
			panic(err)
		}
		id, read := bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 32)
		seed, err := links.SealSubFile(links.Bundle{V: 2, Kind: "routes", Mode: "replace", Sub: id, Revision: 1, Signer: pub, Routes: []links.Route{{ID: "subscription-smoke", Type: "soar", Wire: "soar-v1", Priority: 100, Params: map[string]any{"zone": "tunnel.example.org", "key": make([]byte, 32)}}}}, id, read, key)
		if err != nil {
			panic(err)
		}
		raw, err := links.EncodeInvite(links.Invite{V: 2, Kind: "sub", ID: id, Name: "Example subscription", Sources: []string{"https://example.org/sub"}, ReadKey: read, Required: []string{"sub"}, Seed: seed}, key)
		if err != nil {
			panic(err)
		}
		uri, err := links.InviteURI(raw, "")
		if err != nil {
			panic(err)
		}
		fmt.Print(uri)
		return
	}

	raw, err := links.Encode(links.Bundle{
		V: 2, Kind: "routes", Mode: "merge",
		Routes: []links.Route{{
			ID: "smoke", Type: "soar", Wire: "soar-v1", Priority: 100,
			Params: map[string]any{"zone": "tunnel.example.org", "key": make([]byte, 32)},
		}},
	}, nil)
	if err != nil {
		panic(err)
	}
	uri, err := links.URI(raw, "")
	if err != nil {
		panic(err)
	}
	fmt.Print(uri)
}
