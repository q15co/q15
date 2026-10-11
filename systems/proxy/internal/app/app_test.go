package app

import (
	"strings"
	"testing"

	"github.com/q15co/q15/systems/proxy/internal/config"
)

func TestToProxyRulesCarriesOCISigning(t *testing.T) {
	rules := toProxyRules([]config.ProxyRule{
		{
			Name:       " oci-api ",
			MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
			SignOCI: &config.ProxyOCISign{
				Secret: "oci_api_key",
				KeyID:  "ocid1.t/ocid1.u/12:34:56:78",
			},
		},
		{Name: "plain", MatchHosts: []string{"api.github.com"}},
	})

	if len(rules) != 2 {
		t.Fatalf("expected two rules, got %#v", rules)
	}
	sign := rules[0].SignOCI
	if sign == nil {
		t.Fatal("expected the signing block to reach the proxy")
	}
	if sign.Secret != "oci_api_key" || sign.KeyID != "ocid1.t/ocid1.u/12:34:56:78" {
		t.Fatalf("signing block = %#v, want the configured values", sign)
	}
	if rules[1].SignOCI != nil {
		t.Fatalf("expected no signing block on a plain rule, got %#v", rules[1].SignOCI)
	}
}

func TestValidateArgsAcceptsNoArgs(t *testing.T) {
	if err := validateArgs(nil); err != nil {
		t.Fatalf("validateArgs(nil) error = %v", err)
	}
}

func TestValidateArgsRejectsUnexpectedArgs(t *testing.T) {
	err := validateArgs([]string{"serve"})
	if err == nil {
		t.Fatal("validateArgs() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "accepts no arguments") {
		t.Fatalf("unexpected error: %v", err)
	}
}
