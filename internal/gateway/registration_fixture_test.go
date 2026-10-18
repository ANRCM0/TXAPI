package gateway

import "minigateway/internal/protocol"

func newRegisteredGateway(opts Options) (*Gateway, error) {
	if opts.Protocols == nil {
		opts.Protocols = protocol.Protocols(nil)
	}
	if opts.Operations == nil {
		opts.Operations = protocol.Operations()
	}
	return New(opts)
}
