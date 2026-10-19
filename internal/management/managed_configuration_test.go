package management

import (
	"context"
	"errors"
	cfg "minigateway/internal/foundation/contracts/configuration/v1"
	managed "minigateway/internal/foundation/contracts/configuration/v2"
	"testing"
)

type managedRepositoryStub struct {
	managed.Repository
	saves int
	err   error
}

func (r *managedRepositoryStub) CreateManagedChannel(context.Context, *managed.ManagedChannelInput) (*managed.ManagedChannel, error) {
	r.saves++
	return &managed.ManagedChannel{Credential: managed.ManagedCredential{CredentialID: 3, Revision: 1}}, r.err
}

func TestManagedSaveUsesExistingPublicationAndApplyNeverRepeatsMutation(t *testing.T) {
	service := NewConfiguration(nil, nil, nil)
	_, change, err := service.CreateManagedChannel(context.Background(), &managed.ManagedChannelInput{})
	if !errors.Is(err, managed.ErrUnavailable) || change.Saved {
		t.Fatalf("disabled: %+v %v", change, err)
	}
	repo := &managedRepositoryStub{}
	service.SetManagedRepository(repo)
	service.channels.apply = func() error { return errors.New("private-provider-secret") }
	_, change, err = service.CreateManagedChannel(context.Background(), &managed.ManagedChannelInput{})
	if err != nil || !change.Saved || change.Publication.AppliedRevision == change.Publication.DesiredRevision {
		t.Fatalf("saved pending: %+v %v", change, err)
	}
	service.channels.apply = nil
	status := service.Apply()
	if repo.saves != 1 || status.Channels.DesiredRevision != change.Publication.DesiredRevision || status.Channels.AppliedRevision != status.Channels.DesiredRevision {
		t.Fatalf("replayed save: %+v saves=%d", status, repo.saves)
	}
	repo.err = &cfg.SavedError{Cause: errors.New("cache refresh failed")}
	_, change, err = service.CreateManagedChannel(context.Background(), &managed.ManagedChannelInput{})
	if err == nil || !change.Saved || change.Publication.DesiredRevision != status.Channels.DesiredRevision+1 {
		t.Fatalf("committed error lost: %+v %v", change, err)
	}
	repo.err = managed.ErrConflict
	before := service.Status()
	_, change, err = service.CreateManagedChannel(context.Background(), &managed.ManagedChannelInput{})
	if !errors.Is(err, managed.ErrConflict) || change.Saved || service.Status() != before {
		t.Fatalf("rollback published: %+v %v", change, err)
	}
}
