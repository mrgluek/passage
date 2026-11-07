package tunnel

import "testing"

func TestSubscriptionNameFollowsTheFile(t *testing.T) {
	s, _, b := subFixture(t)
	got, err := ApplySubFile(Config{Subscriptions: []Subscription{s}}, s, b)
	if err != nil || got.Subscriptions[0].Name != "Example" {
		t.Fatal("a file without a name must keep the stored one", err)
	}
	b.Revision, b.Name = 2, "Renamed"
	got, err = ApplySubFile(got, got.Subscriptions[0], b)
	if err != nil || got.Subscriptions[0].Name != "Renamed" {
		t.Fatalf("name after a renamed file: %q %v", got.Subscriptions[0].Name, err)
	}
	b.Revision, b.Name = 3, ""
	got, err = ApplySubFile(got, got.Subscriptions[0], b)
	if err != nil || got.Subscriptions[0].Name != "Renamed" {
		t.Fatal("an empty name in a later file must not erase the stored one", err)
	}
	b.Revision, b.Name = 4, "bad\xffname"
	got, err = ApplySubFile(got, got.Subscriptions[0], b)
	if err != nil || got.Subscriptions[0].Name != "Renamed" {
		t.Fatal("an invalid name must be ignored", err)
	}
}
