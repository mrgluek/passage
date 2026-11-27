package tunnel

import (
	"fmt"
	"testing"

	links "github.com/mrgluek/passage-link/link"
)

func TestSubscriptionSitesFromTheTrustedAuthorOnly(t *testing.T) {
	sub, _, bundle := subFixture(t)
	bundle.Sites = []links.SiteGroup{{Via: "smart", Hosts: []string{"example.org"}}, {Via: "ru", Hosts: []string{"ru.example.org"}}}
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil || len(cfg.Subscriptions[0].Sites) != 0 || len(cfg.SiteGroups()) != 0 {
		t.Fatal("an untrusted subscription set sites", err)
	}
	trustSubscription(t, sub)
	cfg, err = ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil || fmtSites(cfg.SiteGroups()) != "[{smart [example.org]}]" {
		t.Fatalf("%v %v", cfg.SiteGroups(), err)
	}
	// A later file without the key removes them; "ru" was ignored.
	bundle.Revision++
	bundle.Sites = nil
	cfg, err = ApplySubFile(cfg, cfg.Subscriptions[0], bundle)
	if err != nil || len(cfg.SiteGroups()) != 0 {
		t.Fatalf("sites kept: %v %v", cfg.SiteGroups(), err)
	}
}

func fmtSites(g []links.SiteGroup) string { return fmt.Sprint(g) }

// A copy stored by a release that did not read the field applies again.
func TestSchemaBumpAppliesSitesToTheSameRevision(t *testing.T) {
	sub, _, bundle := subFixture(t)
	trustSubscription(t, sub)
	cfg := appliedByOlderRelease(t, sub, bundle)
	cfg.Subscriptions[0].Schema = 1
	bundle.Sites = []links.SiteGroup{{Via: "tunnel", Hosts: []string{"example.org"}}}
	next, err := ApplySubFile(cfg, cfg.Subscriptions[0], bundle)
	if err != nil || fmtSites(next.SiteGroups()) != "[{tunnel [example.org]}]" || next.Subscriptions[0].Schema != 2 {
		t.Fatalf("%v %v", next.SiteGroups(), err)
	}
	if sameApplied(cfg, next, 0) {
		t.Fatal("new sites would not restart the router")
	}
	if subscriptionSchema < 2 {
		t.Fatal("schema not bumped")
	}
}

func TestResubscribeKeepsSites(t *testing.T) {
	sub, key, bundle := subFixture(t)
	trustSubscription(t, sub)
	bundle.Sites = []links.SiteGroup{{Via: "smart", Hosts: []string{"example.org"}}}
	cfg, err := ApplySubFile(Config{Subscriptions: []Subscription{sub}}, sub, bundle)
	if err != nil {
		t.Fatal(err)
	}
	result, err := SubscribeLink(cfg, subInvite(t, sub, key, nil), false)
	if err != nil || fmtSites(result.Config.SiteGroups()) != "[{smart [example.org]}]" {
		t.Fatalf("sites lost on re-subscribe: %v %v", result.Config.SiteGroups(), err)
	}
}
