package main

import (
	"errors"
	"net/url"
	"strings"
)

const maxLaunchLink = 16384

var startupLink string

func launchLink(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if len(args) != 1 {
		return "", errors.New("expected one Passage link")
	}
	text := args[0]
	if len(text) > maxLaunchLink {
		return "", errors.New("link is too long")
	}
	u, err := url.Parse(text)
	if err != nil || !strings.EqualFold(u.Scheme, "passage") || !strings.HasPrefix(strings.ToLower(text), "passage://") {
		return "", errors.New("invalid Passage link")
	}
	return text, nil
}

func launchImport(args []string) string {
	text, err := launchLink(args)
	if err != nil {
		return "passage://invalid"
	}
	return text
}
