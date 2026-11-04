package main

import (
	"fmt"

	links "github.com/mrgluek/passage-link/link"
)

func main() {
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
