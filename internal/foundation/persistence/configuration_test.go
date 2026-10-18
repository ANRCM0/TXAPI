package persistence

import (
	"errors"
	"testing"
	"time"

	cfg "minigateway/internal/foundation/contracts/configuration/v1"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

type configurationFixture struct {
	ConfigurationBackend
	channel model.Channel
}

func (f *configurationFixture) CreateChannel(in *store.ChannelInput) (*model.Channel, error) {
	in.Models[0] = "backend-mutation"
	in.ModelMapping["public"] = "backend-mutation"
	return &f.channel, &store.SavedConfigurationError{Err: errors.New("private failure")}
}

func (f *configurationFixture) UpsertModelPrice(in *model.ModelPrice) error {
	in.UpdatedAt = time.Unix(500, 0)
	return nil
}

func TestConfigurationBridgePreservesCommittedErrorAndOwnsValues(t *testing.T) {
	backend := &configurationFixture{channel: model.Channel{ID: 11, Models: model.StringList{"public"}, ModelMapping: model.StringMap{"public": "native"}, Credentials: []model.Credential{{ID: 12, APIKey: "one-time-created-key"}}}}
	port := Configuration(backend)
	in := &cfg.ChannelInput{Models: []string{"public"}, ModelMapping: map[string]string{"public": "native"}}
	out, err := port.CreateChannel(in)
	var saved *cfg.SavedError
	var original *store.SavedConfigurationError
	if !errors.As(err, &saved) || !errors.As(err, &original) || out == nil || out.ID != 11 {
		t.Fatalf("committed-save classification or data lost: %+v %v", out, err)
	}
	if in.Models[0] != "public" || in.ModelMapping["public"] != "native" {
		t.Fatal("backend mutated a public input")
	}
	out.Models[0], out.ModelMapping["public"], out.Credentials[0].APIKey = "changed", "changed", "changed"
	if backend.channel.Models[0] != "public" || backend.channel.ModelMapping["public"] != "native" || backend.channel.Credentials[0].APIKey != "one-time-created-key" {
		t.Fatal("management projection shared storage references")
	}
	price := &cfg.ModelPrice{Model: "m", InputPricePer1K: 1}
	if err := port.UpsertModelPrice(price); err != nil || !price.UpdatedAt.Equal(time.Unix(500, 0)) {
		t.Fatal("saved price timestamp lost across the bridge")
	}
}
