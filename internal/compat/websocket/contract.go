// Package websocket bridges the bounded transport to the public execution port.
package websocket

import (
	"context"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
	transport "minigateway/internal/transport/websocket"
)

type ContractConnection struct {
	connection *transport.Conn
	id         v2.ConnectionID
}

func Adapt(id v2.ConnectionID, connection *transport.Conn) v2.Connection {
	if connection == nil {
		return nil
	}
	return ContractConnection{connection: connection, id: id}
}

func (c ContractConnection) ID() v2.ConnectionID { return c.id }
func (c ContractConnection) Read(ctx context.Context) (v2.Frame, error) {
	typ, data, err := c.connection.Read(ctx)
	return v2.Frame{Type: uint8(typ), Data: data}, err
}
func (c ContractConnection) Write(ctx context.Context, frame v2.Frame) (int, error) {
	if err := c.connection.Write(ctx, transport.MessageType(frame.Type), frame.Data); err != nil {
		return 0, err
	}
	return len(frame.Data), nil
}
func (c ContractConnection) Close() error {
	c.connection.Close()
	return nil
}

var _ v2.Connection = ContractConnection{}
