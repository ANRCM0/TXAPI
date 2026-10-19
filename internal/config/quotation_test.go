package config

import "testing"

func TestQuotationIsOptInAndKeepsExactIntegerConfiguration(t *testing.T) {
	c, err := Load(writeCfg(t, minimalCfg))
	if err != nil || c.Quotation.Enabled {
		t.Fatalf("quotation must default off: %v %+v", err, c)
	}
	body := minimalCfg + `quotation:
  enabled: true
  currency: ACCOUNT_UNIT
  prices:
    - model: m
      uncached_input_units_per_1k: "1000000000000000000"
      cached_input_units_per_1k: "0"
      cache_write_input_units_per_1k: "1"
      output_units_per_1k: "999999999999999999"
  group_rate_units:
    default: "1000000000"
`
	c, err = Load(writeCfg(t, body))
	if err != nil || !c.Quotation.Enabled || c.Quotation.Prices[0].UncachedInputUnitsPer1K != "1000000000000000000" || c.Quotation.Prices[0].OutputUnitsPer1K != "999999999999999999" {
		t.Fatalf("quotation configuration lost integer precision: %v %+v", err, c)
	}
	for _, body := range []string{
		"quotation:\n  enabled: true\n",
		"quotation:\n  enabled: true\n  currency: UNIT\n  prices: []\n  group_rate_units: {default: '1000000000'}\n",
	} {
		if _, err := Load(writeCfg(t, minimalCfg+body)); err == nil {
			t.Fatal("incomplete enabled quotation was accepted")
		}
	}
}
