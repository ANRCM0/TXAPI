package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/model"
)

// ResponseTarget is an application-owned continuity constraint, never client
// routing authority. Ordinary model authorization and capability checks still run.
type ResponseTarget = execution.TargetConstraint

func responseTarget(ch *channel, cred *model.Credential, name string) ResponseTarget {
	actual := name
	if mapped := ch.mapping[name]; mapped != "" {
		actual = mapped
	}
	raw, _ := json.Marshal([]string{ch.typ, ch.baseURL, cred.APIKey, actual})
	if isManaged(cred) {
		raw, _ = json.Marshal([]string{cred.Source, cred.AccountID, strconv.FormatInt(cred.BindingRevision, 10), strconv.FormatInt(cred.AccountRevision, 10), strconv.FormatInt(cred.CredentialRevision, 10), managedOrigin, actual})
	}
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
