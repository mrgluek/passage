package tunnel

import (
	"strings"
	"testing"
)

func TestLiveEventsRedactSecrets(t *testing.T) {
	log := newEventLog(4)
	log.add("request failed: token=%s", "private-value")
	log.add("request failed: %s", "https://example.org/private")
	for _, event := range log.last(4) {
		if strings.Contains(event.Msg, "private-value") || strings.Contains(event.Msg, "example.org") {
			t.Fatal(event.Msg)
		}
	}
}
