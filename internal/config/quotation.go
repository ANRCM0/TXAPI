package config

import "fmt"

// QuotationConfig is startup-only selling metadata, separate from billing and
// provider-cost declarations. Values use exact integer strings, not floats.
type QuotationConfig struct {
	Enabled        bool                   `yaml:"enabled"`
	Currency       string                 `yaml:"currency"`
	Prices         []QuotationPriceConfig `yaml:"prices"`
	GroupRateUnits map[string]string      `yaml:"group_rate_units"`
}

type QuotationPriceConfig struct {
	Model                     string `yaml:"model"`
	UncachedInputUnitsPer1K   string `yaml:"uncached_input_units_per_1k"`
	CachedInputUnitsPer1K     string `yaml:"cached_input_units_per_1k"`
	CacheWriteInputUnitsPer1K string `yaml:"cache_write_input_units_per_1k"`
	OutputUnitsPer1K          string `yaml:"output_units_per_1k"`
}

func (c QuotationConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Currency == "" || len(c.Prices) < 1 || len(c.Prices) > 256 || len(c.GroupRateUnits) < 1 || len(c.GroupRateUnits) > 64 {
		return fmt.Errorf("quotation.enabled requires explicit currency, 1..256 prices and 1..64 group rates")
	}
	// The billing owner checks canonical units, profiles and the final document
	// before any immutable write or publication during main composition.
	return nil
}
