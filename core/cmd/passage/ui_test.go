package main

import (
	"strings"
	"testing"
)

func TestStatusDotRipple(t *testing.T) {
	for _, css := range []string{
		".dot.flow::after",
		"animation: dot-ripple 1.2s ease-out infinite",
		"@keyframes dot-ripple",
		"transform: scale(2.2); opacity: 0",
		".window-hidden .dot.flow::after, .hidden .dot.flow::after { animation: none; }",
		"@media (prefers-reduced-motion: reduce) { .dot.flow::after { animation: none; } }",
	} {
		if !strings.Contains(indexHTML, css) {
			t.Errorf("status dot CSS missing %q", css)
		}
	}
}
