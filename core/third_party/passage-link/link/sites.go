package link

import (
	"errors"
	"strings"
)

const (
	// MaxSiteGroups and MaxSiteHosts keep every CBOR array under the 128-element
	// decoder limit that all app versions share.
	MaxSiteGroups = 16
	MaxSiteHosts  = 120
)

// SiteGroup is a list of host names and the kind of route that may carry them.
type SiteGroup struct {
	Via   string   `json:"via" cbor:"via"`
	Hosts []string `json:"hosts" cbor:"hosts"`
}

// Known values of SiteGroup.Via. "ru" is reserved: accepted, ignored by clients.
const (
	SiteViaSmart  = "smart"
	SiteViaTunnel = "tunnel"
	SiteViaRU     = "ru"
)

// ValidateSites checks the group and host limits and every host name. An
// unknown Via is accepted so that a later app version can add kinds; clients
// ignore a group they do not know. Publishers use ValidateSitesStrict.
func ValidateSites(groups []SiteGroup) error {
	if len(groups) > MaxSiteGroups {
		return errors.New("too many site groups")
	}
	for _, g := range groups {
		if !validSiteVia(g.Via) {
			return errors.New("invalid site group kind")
		}
		if len(g.Hosts) < 1 || len(g.Hosts) > MaxSiteHosts {
			return errors.New("invalid number of site hosts")
		}
		for _, h := range g.Hosts {
			if err := ValidateSiteHost(h); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateSitesStrict is ValidateSites plus a check that every kind is known.
func ValidateSitesStrict(groups []SiteGroup) error {
	if err := ValidateSites(groups); err != nil {
		return err
	}
	for _, g := range groups {
		if g.Via != SiteViaSmart && g.Via != SiteViaTunnel && g.Via != SiteViaRU {
			return errors.New("unknown site group kind")
		}
	}
	return nil
}

func validSiteVia(via string) bool {
	if len(via) < 1 || len(via) > 16 {
		return false
	}
	for _, c := range via {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// ValidateSiteHost accepts a lower-case DNS name with at least two labels. A
// host covers its subdomains, so wildcards, IP addresses and a bare public
// suffix are refused.
func ValidateSiteHost(host string) error {
	if len(host) > 253 || !validZone(host) {
		return errors.New("invalid site host")
	}
	labels := strings.Split(host, ".")
	if last := labels[len(labels)-1]; last[0] >= '0' && last[0] <= '9' {
		return errors.New("site host must not be an IP address")
	}
	if publicSuffixes[host] {
		return errors.New("site host is a public suffix")
	}
	return nil
}

// publicSuffixes is a short list of common multi-label suffixes; a single
// label is already refused by the two-label rule.
var publicSuffixes = map[string]bool{
	"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true, "me.uk": true,
	"com.au": true, "net.au": true, "org.au": true, "com.br": true, "com.cn": true,
	"co.jp": true, "co.kr": true, "co.in": true, "co.nz": true, "co.za": true,
	"com.tr": true, "com.ua": true, "com.pl": true, "com.mx": true, "com.ar": true,
	"com.ru": true, "org.ru": true, "net.ru": true, "msk.ru": true, "spb.ru": true,
	"pp.ru": true, "com.by": true, "com.kz": true, "github.io": true,
	"pages.dev": true, "workers.dev": true, "blogspot.com": true, "herokuapp.com": true,
	"appspot.com": true, "web.app": true, "netlify.app": true, "vercel.app": true,
	"cloudfront.net": true, "azurewebsites.net": true, "s3.amazonaws.com": true,
}
