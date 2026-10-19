// Package upstreamauth adds a narrow, trusted host inference credential port.
package upstreamauth

import (
	"context"
	"time"

	v1 "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

const Version = "2"

type AccessRequest struct {
	CredentialID               uint
	ExpectedBindingRevision    int64
	AccountID                  string
	ExpectedRevision           int64
	ExpectedCredentialRevision int64
	OwnerID                    uint
	TokenID                    uint
	Model                      string
}

type AccessCredential struct {
	AccountID          string
	Revision           int64
	CredentialRevision int64
	GateEpoch          int64
	AccessToken        v1.Secret
	ExpiresAt          time.Time
}

// AcquireAccess serializes credential issuance with account and binding writes.
// A successful commit authorizes this attempt; later revocation does not retract
// material already issued. The host never caches or reuses it across attempts.
type InferenceCredentials interface {
	AcquireAccess(context.Context, AccessRequest) (AccessCredential, error)
}
