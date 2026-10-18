package controlplane

import (
	"errors"
	"testing"

	control "minigateway/internal/foundation/contracts/control/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
)

type repositoryFixture struct {
	control.Repository
	issued int
}

func (r *repositoryFixture) IssueToken(owner uint, in *control.TokenInput) (*control.Token, error) {
	r.issued++
	return &control.Token{UserID: owner}, nil
}

func (*repositoryFixture) SystemPolicy() (identity.Policy, error) {
	return identity.Policy{Mode: identity.Allowlist, Models: []string{"allowed"}}, nil
}

func (*repositoryFixture) ModelChannels() []control.ModelChannel {
	return []control.ModelChannel{{Enabled: true, Models: []string{"*", "allowed", "other"}}, {Models: []string{"disabled"}}}
}

func TestOwnerKeyAndModelRulesRunWithoutHTTP(t *testing.T) {
	repository := &repositoryFixture{}
	service := New(repository)
	if _, err := service.CreateOwnedKey(8, &control.TokenInput{UserID: 9}); !errors.Is(err, control.ErrForbidden) || repository.issued != 0 {
		t.Fatal("a foreign owner reached key persistence")
	}
	if key, err := service.CreateOwnedKey(8, &control.TokenInput{}); err != nil || key.UserID != 8 || repository.issued != 1 {
		t.Fatal("trusted owner was not preserved")
	}
	allowed, err := service.ModelsForPolicy(identity.Policy{Mode: identity.All})
	if err != nil || len(allowed.Models) != 1 || allowed.Models[0] != "allowed" {
		t.Fatalf("system policy or wildcard projection was bypassed: %+v %v", allowed, err)
	}
	denied, err := service.ModelsForPolicy(identity.Policy{Mode: identity.Allowlist, Models: []string{}})
	if err != nil || len(denied.Models) != 0 || denied.Policy.Allows("allowed") {
		t.Fatal("an empty allowlist gained access")
	}
}
