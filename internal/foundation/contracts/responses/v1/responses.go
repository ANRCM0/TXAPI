// Package responses defines the foreground application command and delivery ports.
package responses

import (
	"context"
	"encoding/json"
	execution "minigateway/internal/foundation/contracts/execution/v1"
)

const Version = "1"
const MaxRequestBytes = 1 << 20

type Action string

const (
	CreateResponse     Action = "response.create"
	GetResponse        Action = "response.get"
	DeleteResponse     Action = "response.delete"
	ListInputItems     Action = "response.inputs"
	CreateConversation Action = "conversation.create"
	GetConversation    Action = "conversation.get"
	UpdateConversation Action = "conversation.update"
	DeleteConversation Action = "conversation.delete"
	ListItems          Action = "conversation.items"
	AddItems           Action = "conversation.items.add"
	GetItem            Action = "conversation.item.get"
	DeleteItem         Action = "conversation.item.delete"
)

type Resource string

const (
	ResponseResource     Resource = "response"
	ConversationResource Resource = "conversation"
)

type PageQuery struct {
	After   string
	Limit   int
	Order   string
	Invalid bool
}

// Payload is supplied by the compatibility entry and returns an owned, bounded
// request snapshot. It is read synchronously at most once, after application guards.
type Payload interface{ Read() ([]byte, error) }
type Command struct {
	Action          Action
	Resource        Resource
	ID              string
	ItemID          string
	Payload         Payload
	HasQuery        bool
	Page            PageQuery
	ExpectedVersion *uint64
	InvalidVersion  bool
	IdempotencyKeys []string
}
type Metadata struct {
	GenerationID      string
	ResponseID        string
	Idempotency       string
	RetryAfterSeconds int
	Version           uint64
	NoStore           bool
}
type Problem struct {
	Code     string
	Message  string
	Metadata Metadata
}
type DocumentKind string

const (
	NativeDocument              DocumentKind = "native"
	ConversationDocument        DocumentKind = "conversation"
	ItemsDocument               DocumentKind = "items"
	DeletedResponseDocument     DocumentKind = "response.deleted"
	DeletedConversationDocument DocumentKind = "conversation.deleted"
)

type Conversation struct {
	ID        string
	CreatedAt int64
	Metadata  map[string]string
}
type Page struct {
	Items   []json.RawMessage
	FirstID *string
	LastID  *string
	HasMore bool
}
type Document struct {
	Kind         DocumentKind
	Raw          json.RawMessage
	Conversation Conversation
	Page         Page
	ID           string
}

// Delivery maps semantic documents/problems and metadata to one bounded output.
// It must not reenter or close its owning Call from a synchronous callback.
type Delivery interface {
	execution.DeadlineOutput
	Annotate(Metadata)
	Problem(Problem)
	Document(Document)
}

// A Call owns delivery capacity until Close joins its single Handle. Close is
// idempotent; reuse cannot start another generation or reuse an authority snapshot.
type Call interface {
	Handle(Command, Delivery)
	Close()
}
type Service interface {
	Begin(context.Context, Action) (Call, *Problem)
}
