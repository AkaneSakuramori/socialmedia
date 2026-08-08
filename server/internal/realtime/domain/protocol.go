// Package domain defines the realtime protocol contract (API.md §16–§18):
// the frame envelope, the event vocabulary, and the server-initiated close
// codes. Delivery-layer code depends on these types; no I/O lives here.
package domain

import (
	"encoding/json"
	"errors"
	"time"
)

// ProtocolVersion is the frame envelope version (API.md §16.2).
const ProtocolVersion = 1

// Subprotocol is the negotiated WebSocket subprotocol (API.md §16.1). A client
// that does not offer it is rejected before any payload is exchanged.
const Subprotocol = "chat.v1"

// Frame is the wire envelope for every client→server and server→client
// message (API.md §16.2). For C2S frames Seq is nil and ID is client-supplied
// (echoed in acks); for S2C frames Seq is the server-assigned per-connection
// monotonic sequence and ID is server-generated.
type Frame struct {
	Version int             `json:"v"`
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Seq     *int64          `json:"seq"`
	At      time.Time       `json:"at"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Encode serializes the frame for the wire.
func (f *Frame) Encode() ([]byte, error) {
	f.Version = ProtocolVersion
	return json.Marshal(f)
}

// Decode parses a wire frame and validates the envelope (API.md §16.2):
// the protocol version must match, the type must be non-empty, and if data is
// present it must be a JSON object.
func Decode(b []byte) (*Frame, error) {
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Version != ProtocolVersion {
		return nil, errors.New("realtime: unsupported protocol version")
	}
	if f.Type == "" {
		return nil, errors.New("realtime: frame missing type")
	}
	if len(f.Data) > 0 {
		var probe map[string]any
		if err := json.Unmarshal(f.Data, &probe); err != nil {
			return nil, errors.New("realtime: frame data must be a JSON object")
		}
	}
	return &f, nil
}

// Client→server event names (API.md §17).
const (
	EventHello            = "hello"             // §17.1
	EventResume           = "resume"            // §17.2
	EventSubscribe        = "subscribe"         // §17.3
	EventUnsubscribe      = "unsubscribe"       // §17.3
	EventMessageSend      = "message.send"      // §17.4 ◎
	EventMessageEdit      = "message.edit"      // §17.5 ◎
	EventMessageDelete    = "message.delete"    // §17.6 ◎
	EventReactionAdd      = "reaction.add"      // §17.7 ◎
	EventReactionRemove   = "reaction.remove"   // §17.7 ◎
	EventReceiptRead      = "receipt.read"      // §17.8 ◎
	EventReceiptDelivered = "receipt.delivered" // §17.9
	EventTypingStart      = "typing.start"      // §17.10 (M4)
	EventTypingStop       = "typing.stop"       // §17.10 (M4)
	EventPresenceUpdate   = "presence.update"   // §17.11 (M4)
	EventPing             = "ping"              // §17.12
	EventPong             = "pong"              // §17.12
	EventAck              = "ack"               // §17.13
)

// Server→client event names (API.md §18).
const (
	EventHelloAck               = "hello_ack"            // §18.1
	EventResumeAck              = "resume_ack"           // §18.2
	EventResumeRejected         = "resume_rejected"      // §18.2
	EventServerAck              = "ack"                  // §18.3
	EventError                  = "error"                // §18.4
	EventMessageCreated         = "message.created"      // §18.5
	EventMessageEdited          = "message.edited"       // §18.6
	EventMessageDeleted         = "message.deleted"      // §18.7
	EventReactionAdded          = "reaction.added"       // §18.8
	EventReactionRemoved        = "reaction.removed"     // §18.8
	EventServerReceiptRead      = "receipt.read"         // §18.9
	EventServerReceiptDelivered = "receipt.delivered"    // §18.10
	EventTypingIndicator        = "typing.indicator"     // §18.11 (M4)
	EventPresenceChanged        = "presence.changed"     // §18.12 (M4)
	EventConvCreated            = "conversation.created" // §18.13
	EventConvUpdated            = "conversation.updated" // §18.14
	EventMembership             = "membership.changed"   // §18.15
	EventConvDeleted            = "conversation.deleted" // §18.16
	EventMediaReady             = "media.ready"          // §18.17
	EventNotification           = "notification.created" // §18.18
	EventSessionRevoked         = "session.revoked"      // §18.19
	EventSettingsUpdated        = "settings.updated"     // §18.20
	EventServerShutdown         = "server.shutdown"      // §18.21
	EventFlagUpdated            = "flag.updated"         // §18.22
)

// CloseCode is a server-initiated socket close code (API.md §18.23). The
// values are app-level status codes in the 3xxx–4xxx range the WebSocket spec
// reserves for applications; only these codes may be used — never invented.
type CloseCode int

const (
	CloseAuthInvalid    CloseCode = 4401 // auth/token invalid → re-auth + reconnect
	CloseSessionRevoked CloseCode = 4403 // session revoked (this device) → force logout
	CloseRateLimit      CloseCode = 4501 // rate-limit abuse → back off 60 s
	CloseProtocol       CloseCode = 4502 // invalid frame / protocol violation → reconnect
	CloseSlowConsumer   CloseCode = 4510 // slow consumer → resume handoff (ENGINEERING.md §18.3)
	CloseServerRestart  CloseCode = 1012 // server restart → respect server.shutdown
	ClosePolicy         CloseCode = 1008 // policy violation
)

// String returns the API.md §18.23 meaning of a close code.
func (c CloseCode) String() string {
	switch c {
	case CloseAuthInvalid:
		return "auth/token invalid"
	case CloseSessionRevoked:
		return "session revoked"
	case CloseRateLimit:
		return "rate limit abuse"
	case CloseProtocol:
		return "invalid frame / protocol violation"
	case CloseSlowConsumer:
		return "slow consumer"
	case CloseServerRestart:
		return "server restart"
	case ClosePolicy:
		return "policy violation"
	default:
		return "unknown close code"
	}
}
