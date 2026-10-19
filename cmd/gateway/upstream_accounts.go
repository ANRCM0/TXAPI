package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"time"

	"minigateway/internal/compat/oauthloopback"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	"minigateway/internal/foundation/upstreamhttp"
	"minigateway/internal/protocol/openaiauth"
	"minigateway/internal/store"
	"minigateway/internal/upstreamidentity"
)

type upstreamAccountOptions struct {
	action, id, name string
	revision         int64
	limit, offset    int
}

func upstreamAccountFlags(flags *flag.FlagSet) *upstreamAccountOptions {
	options := &upstreamAccountOptions{}
	flags.StringVar(&options.action, "upstream-account", "", "local account operation: authorize, reauthorize, list, refresh, disable")
	flags.StringVar(&options.id, "upstream-account-id", "", "managed account ID")
	flags.StringVar(&options.name, "upstream-account-name", "", "account display name for authorization")
	flags.Int64Var(&options.revision, "upstream-account-revision", 0, "expected metadata revision from list")
	flags.IntVar(&options.limit, "upstream-account-limit", 50, "list page size (1..100)")
	flags.IntVar(&options.offset, "upstream-account-offset", 0, "list offset (0..10000)")
	return options
}

func (options upstreamAccountOptions) validate(otherCommand bool) error {
	if options.action == "" {
		if options.id != "" || options.name != "" || options.revision != 0 || options.limit != 50 || options.offset != 0 {
			return auth.ErrInvalid
		}
		return nil
	}
	if otherCommand {
		return auth.ErrInvalid
	}
	if options.action != "list" && (options.limit != 50 || options.offset != 0) {
		return auth.ErrInvalid
	}
	switch options.action {
	case "authorize":
		if options.id != "" || options.revision != 0 || !auth.ValidName(options.name) {
			return auth.ErrInvalid
		}
	case "reauthorize", "refresh", "disable":
		if !auth.ValidAccountID(options.id) || options.revision <= 0 {
			return auth.ErrInvalid
		}
		if options.action == "reauthorize" {
			if options.name != "" && !auth.ValidName(options.name) {
				return auth.ErrInvalid
			}
		} else if options.name != "" {
			return auth.ErrInvalid
		}
	case "list":
		if options.id != "" || options.revision != 0 || options.name != "" || options.limit < 1 || options.limit > 100 || options.offset < 0 || options.offset > 10000 {
			return auth.ErrInvalid
		}
	default:
		return auth.ErrInvalid
	}
	return nil
}

// The client is trusted assembly input; production always supplies nil. There is
// no deployment setting that can redirect authorization or relax verification.
func runUpstreamAccount(parent context.Context, st *store.Store, options upstreamAccountOptions, output io.Writer, client *http.Client) error {
	if parent == nil || st == nil || output == nil || options.action == "" || options.validate(false) != nil {
		return auth.ErrInvalid
	}
	timeout := 5 * time.Second
	switch options.action {
	case "authorize", "reauthorize":
		timeout = 11 * time.Minute
	case "refresh":
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if options.action == "authorize" || options.action == "reauthorize" || options.action == "refresh" {
		if !st.CredentialEncryptionReady(ctx) {
			return auth.ErrEncryptionRequired
		}
	}
	provider := openaiauth.New()
	caller := upstreamhttp.NewCaller(client)
	defer caller.Close()
	manager, err := upstreamidentity.New(st.UpstreamAccounts(), provider, caller, oauthloopback.New(provider))
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	var result any
	switch options.action {
	case "authorize", "reauthorize":
		result, err = manager.Authorize(ctx, options.name, options.id, options.revision, func(address auth.Secret) error {
			return encoder.Encode(struct {
				AuthorizationURL string `json:"authorization_url"`
			}{address.Value()})
		})
	case "list":
		result, err = manager.List(ctx, options.limit, options.offset)
	case "refresh":
		result, err = manager.Refresh(ctx, options.id, options.revision)
	case "disable":
		result, err = manager.Disable(ctx, options.id, options.revision)
	}
	if err != nil {
		return err
	}
	if err := encoder.Encode(result); err != nil {
		return auth.ErrUnavailable
	}
	return nil
}
