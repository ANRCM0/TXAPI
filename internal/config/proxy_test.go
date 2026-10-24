package config

import (
	"strings"
	"testing"
)

func TestTrustedProxyCIDRs(t *testing.T) {
	for _, body := range []string{"", "  trusted_proxy_cidrs: []\n", "  trusted_proxy_cidrs: [127.0.0.1/32, '::1/128', 192.0.2.0/24]\n"} {
		if _, err := Load(writeCfg(t, minimalCfg+"server:\n"+body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, list := range []string{"[0.0.0.0/0]", "['::/0']", "[localhost]", "['2001:db8::/32','2001:DB8::/32']", "[127.0.0.1]", "[192.0.2.1/24]", "[127.0.0.1/32,127.0.0.1/32]", "['::ffff:192.0.2.1/128']", "['']", "[" + strings.Repeat("127.0.0.1/32,", 32) + "127.0.0.1/32]"} {
		if _, err := Load(writeCfg(t, minimalCfg+"server:\n  trusted_proxy_cidrs: "+list+"\n")); err == nil {
			t.Fatalf("accepted unsafe proxy policy: %s", list)
		}
	}
}
