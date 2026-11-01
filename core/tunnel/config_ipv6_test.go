package tunnel

import (
	"encoding/json"
	"testing"
)

func TestLinkServerIPv6(t *testing.T) {
	fields := "v=1&zone=t.example.org&key=" + testKey
	for _, prefix := range []string{"passage://tunnel?", "velta://tunnel?", "https://" + LinkHost + LinkPath + "#"} {
		for _, tc := range []struct {
			query string
			want  bool
		}{
			{"", false},
			{"&ipv6=0", false},
			{"&ipv6=1", true},
		} {
			t.Run(prefix+tc.query, func(t *testing.T) {
				cfg, err := ParseLink(prefix + fields + tc.query)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.TunnelIPv6 != tc.want {
					t.Fatalf("server IPv6=%v, want %v", cfg.TunnelIPv6, tc.want)
				}
				data, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				var saved Config
				if err := json.Unmarshal(data, &saved); err != nil {
					t.Fatal(err)
				}
				cfg, err = saved.Normalize()
				if err != nil || cfg.TunnelIPv6 != tc.want {
					t.Fatalf("capability lost after JSON round trip: %+v, %v", cfg, err)
				}
			})
		}
	}
	for _, query := range []string{"&ipv6=", "&ipv6=2", "&ipv6=true", "&ipv6=1&ipv6=0"} {
		if _, err := ParseLink("passage://tunnel?" + fields + query); err == nil {
			t.Errorf("accepted invalid capability %q", query)
		}
	}
}

func TestServerIPv6DoesNotFilterResolvers(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg, err := (Config{Zone: "t.example.org", Key: testKey, TunnelIPv6: enabled,
			Resolvers: []string{"[2001:db8::1]:53"}}).Normalize()
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Resolvers) != 1 || cfg.Resolvers[0] != "[2001:db8::1]:53" {
			t.Fatalf("server capability must not alter DNS paths: %+v", cfg)
		}
	}
}
