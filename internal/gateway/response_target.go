package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"minigateway/internal/model"
)

// ResponseTarget is an application-owned continuity constraint, never client
// routing authority. Ordinary model authorization and capability checks still run.
type ResponseTarget struct {
	ChannelID    uint   `json:"channel_id"`
	CredentialID uint   `json:"credential_id"`
	ActualModel  string `json:"actual_model"`
	Fingerprint  string `json:"fingerprint"`
}

func responseTarget(ch *channel, cred *model.Credential, name string) ResponseTarget {
	actual := name
	if mapped := ch.mapping[name]; mapped != "" {
		actual = mapped
	}
	raw, _ := json.Marshal([]string{ch.typ, ch.baseURL, cred.APIKey, actual})
	sum := sha256.Sum256(raw)
	clear(raw)
	return ResponseTarget{ChannelID: uint(ch.id), CredentialID: cred.ID, ActualModel: actual, Fingerprint: hex.EncodeToString(sum[:])}
}

func (g *Gateway) responseChannels(channels []*channel, name string, required *ResponseTarget) []*channel {
	if required == nil {
		return channels
	}
	var out []*channel
	for _, ch := range channels {
		if uint(ch.id) != required.ChannelID {
			continue
		}
		for _, cred := range ch.credentials {
			if responseTarget(ch, &cred, name) == *required {
				copy := *ch
				copy.credentials = []model.Credential{cred}
				out = append(out, &copy)
			}
		}
	}
	return out
}
