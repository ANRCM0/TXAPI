package config

import "fmt"

type BackgroundConfig struct {
	Enabled                       bool   `yaml:"enabled"`
	ChannelIDs                    []uint `yaml:"channel_ids"`
	RetentionSeconds              int    `yaml:"retention_seconds"`
	MetadataSeconds               int    `yaml:"metadata_seconds"`
	InstanceRecords, OwnerRecords int64  `yaml:"-"`
	InstanceBytes, OwnerBytes     int64  `yaml:"-"`
	InstanceActive, OwnerActive   int64  `yaml:"-"`
}

func (c *BackgroundConfig) defaults() {
	if c.RetentionSeconds == 0 {
		c.RetentionSeconds = 600
	}
	if c.MetadataSeconds == 0 {
		c.MetadataSeconds = 86400
	}
	c.InstanceRecords, c.OwnerRecords = 1024, 128
	c.InstanceBytes, c.OwnerBytes = 64<<20, 16<<20
	c.InstanceActive, c.OwnerActive = 16, 2
}
func (c BackgroundConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.RetentionSeconds < 300 || c.RetentionSeconds > 3600 || c.MetadataSeconds < c.RetentionSeconds || c.MetadataSeconds > 7*86400 || len(c.ChannelIDs) == 0 {
		return fmt.Errorf("background retention and explicit channel_ids are required and must be bounded")
	}
	seen := map[uint]bool{}
	for _, id := range c.ChannelIDs {
		if id == 0 || seen[id] {
			return fmt.Errorf("background channel_ids must be unique positive IDs")
		}
		seen[id] = true
	}
	return nil
}
