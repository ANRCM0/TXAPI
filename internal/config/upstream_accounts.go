package config

type UpstreamAccountsConfig struct {
	Inference ManagedInferenceConfig `yaml:"inference"`
}

type ManagedInferenceConfig struct {
	Enabled bool `yaml:"enabled"`
}
