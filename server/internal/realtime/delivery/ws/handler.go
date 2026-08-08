package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	chatdomain "github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

// wireError is the stable wire-level error vocabulary the handler sends in ack
// frames and error frames (API.md §18.3/§18.4). Codes mirror the REST Appendix A
// vocabulary; the raw application error is never exposed.
type wireError struct {
	Code      string `json:"code"`
	Detail    string `json:"detail,omitempty"`
	Retryable bool   `json:"retryable"`
}

// c2sPayloads bind each C2S event's data object (API.md §17).
type helloPayload struct {
	AccessToken   string `json:"access_token"`
	DeviceID      string `json:"device_id"`
	SessionID     int64  `json:"session_id"`
	ClientVersion string `json:"client_version"`
	LastSeq       *int64 `json:"last_seq"`
	LastGlobalSeq *int64 `json:"last_global_seq"`
}

type resumePayload struct {
	LastSeq       int64 `json:"last_seq"`
	LastGlobalSeq int64 `json:"last_global_seq"`
	SessionID     int64 `json:"session_id"`
}

type subscribePayload struct {
	ConversationIDs []int64 `json:"conversation_ids"`
}

type unsubscribePayload struct {
	ConversationID int64 `json:"conversation_id"`
}

type sendMessagePayload struct {
	ClientMsgID    string                  `json:"client_msg_id"`
	ConversationID int64                   `json:"conversation_id"`
	Type           string                  `json:"type"`
	Text           *string                 `json:"text"`
	Media          []chatdomain.Attachment `json:"media"`
	ReplyToSeq     *int64                  `json:"reply_to_seq"`
	Mentions       []int64                 `json:"mentions"`
}

type editMessagePayload struct {
	MessageID      int64 `json:"message_id"`
	ConversationID int64 `json:"conversation_id"`
	Content        struct {
		Text string `json:"text"`
	} `json:"content"`
}

type deleteMessagePayload struct {
	MessageID      int64  `json:"message_id"`
	ConversationID int64  `json:"conversation_id"`
	Mode           string `json:"mode"`
}

type reactionPayload struct {
	MessageID      int64  `json:"message_id"`
	ConversationID int64  `json:"conversation_id"`
	Emoji          string `json:"emoji"`
}

type receiptPayload struct {
	ConversationID int64 `json:"conversation_id"`
	LastReadSeq    int64 `json:"last_read_seq"`
}

// Handler processes inbound frames for connected sockets (the hub's
// FrameHandler). It is thin: it decodes the frame's data, calls the chat
// application service, and acks/errors. Fan-out is not its job — the dispatcher
// delivers committed change_log events; the handler only drives C2S ops
// (ENGINEERING.md §8.4). It never writes to the socket except via c.ack.
type Handler struct {
	chat application.Service
	log  *slog.Logger
	now  func() time.Time
	// MaxGlobalSeqReadDelay is unused in v1; reserved for resume cursor checks.
}

// NewHandler builds the frame handler for the gateway.
func NewHandler(chat application.Service, log *slog.Logger) *Handler {
	return &Handler{chat: chat, log: log, now: time.Now}
}

// HandleFrame dispatches one inbound frame (API.md §17). It returns an error
// only for protocol violations that should close the socket; business failures
// are acked with an error payload and keep the socket open (§18.4).
func (h *Handler) HandleFrame(ctx context.Context, c *Connection, frame *domain.Frame) error {
	switch frame.Type {
	case domain.EventPing:
		// Protocol frame — no ack, no id (API.md §17.12).
		c.sendEvent(domain.EventPong, map[string]any{"ts": h.now().UnixMilli()})
		return nil
	case domain.EventResume:
		return h.handleResume(ctx, c, frame)
	case domain.EventSubscribe:
		return h.handleSubscribe(ctx, c, frame)
	case domain.EventUnsubscribe:
		return h.handleUnsubscribe(c, frame)
	case domain.EventMessageSend:
		return h.handleMessageSend(ctx, c, frame)
	case domain.EventMessageEdit:
		return h.handleMessageEdit(ctx, c, frame)
	case domain.EventMessageDelete:
		return h.handleMessageDelete(ctx, c, frame)
	case domain.EventReactionAdd:
		return h.handleReaction(ctx, c, frame, true)
	case domain.EventReactionRemove:
		return h.handleReaction(ctx, c, frame, false)
	case domain.EventReceiptRead:
		return h.handleReceiptRead(ctx, c, frame)
	case domain.EventReceiptDelivered:
		return h.handleReceiptDelivered(ctx, c, frame)
	case domain.EventTypingStart, domain.EventTypingStop, domain.EventPresenceUpdate:
		// Deferred to M4 (ARCHITECTURE.md §17).
		c.ack(frame.ID, map[string]any{"status": "not_supported"}, "NOT_SUPPORTED")
		return nil
	case domain.EventAck:
		// Client ack of S2C events (API.md §17.13); the dispatcher owns cursor
		// advancement in the resume milestone.
		return nil
	default:
		return fmt.Errorf("realtime: unknown frame type %q", frame.Type)
	}
}

// handleResume is the reconnect continuation (API.md §16.6/§17.2). v1 rejects
// with cursor_too_old so the client runs the snapshot+delta bootstrap; the
// replay path ships with the dispatcher milestone.
func (h *Handler) handleResume(ctx context.Context, c *Connection, frame *domain.Frame) error {
	var p resumePayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	if p.SessionID != 0 && p.SessionID != c.SessionID() {
		return errors.New("realtime: resume session_id mismatch")
	}
	c.sendEvent(domain.EventResumeRejected, map[string]any{
		"reason": "cursor_too_old",
	})
	return nil
}

// handleSubscribe registers the socket for a conversation's live fan-out after
// membership is verified server-side (API.md §17.3, WS-4). A revoked member is
// silently skipped and acked without the conversation id.
func (h *Handler) handleSubscribe(ctx context.Context, c *Connection, frame *domain.Frame) error {
	var p subscribePayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	subscribed := make([]int64, 0, len(p.ConversationIDs))
	for _, convID := range p.ConversationIDs {
		// GetConversation gates on membership (NOT_A_MEMBER for outsiders).
		if _, err := h.chat.GetConversation(ctx, application.GetConversationCommand{
			UserID:         c.UserID(),
			ConversationID: convID,
		}); err != nil {
			if errors.Is(err, chatdomain.ErrNotMember) {
				h.log.Debug("realtime: subscribe denied (not a member)", "conn", c.ID(), "conv", convID)
				continue
			}
			h.ackError(c, frame.ID, wireError{Code: "CONVERSATION_NOT_FOUND", Retryable: true})
			return nil
		}
		c.Subscribe(convID)
		subscribed = append(subscribed, convID)
	}
	c.ack(frame.ID, map[string]any{"subscribed": subscribed}, "")
	return nil
}

// handleUnsubscribe leaves a conversation's fan-out (API.md §17.3). Unsubscribing
// from a conversation never subscribed is a no-op success.
func (h *Handler) handleUnsubscribe(c *Connection, frame *domain.Frame) error {
	var p unsubscribePayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	c.Unsubscribe(p.ConversationID)
	c.ack(frame.ID, map[string]any{"unsubscribed": p.ConversationID}, "")
	return nil
}

func (h *Handler) handleMessageSend(ctx context.Context, c *Connection, frame *domain.Frame) error {
	var p sendMessagePayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	res, err := h.chat.SendMessage(ctx, application.SendMessageCommand{
		UserID:         c.UserID(),
		ConversationID: p.ConversationID,
		ClientMsgID:    p.ClientMsgID,
		Type:           p.Type,
		Text:           p.Text,
		Media:          p.Media,
		ReplyToSeq:     p.ReplyToSeq,
		Mentions:       p.Mentions,
	})
	if err != nil {
		h.ackChatError(c, frame.ID, err)
		return nil
	}
	c.ack(frame.ID, map[string]any{"status": "sent", "message": res.View}, "")
	return nil
}

func (h *Handler) handleMessageEdit(ctx context.Context, c *Connection, frame *domain.Frame) error {
	var p editMessagePayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	res, err := h.chat.EditMessage(ctx, application.EditMessageCommand{
		UserID:    c.UserID(),
		MessageID: p.MessageID,
		NewText:   p.Content.Text,
	})
	if err != nil {
		h.ackChatError(c, frame.ID, err)
		return nil
	}
	c.ack(frame.ID, map[string]any{"status": "edited", "message": res}, "")
	return nil
}

func (h *Handler) handleMessageDelete(ctx context.Context, c *Connection, frame *domain.Frame) error {
	var p deleteMessagePayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	mode := p.Mode
	if mode == "" {
		mode = "all"
	}
	res, err := h.chat.DeleteMessage(ctx, application.DeleteMessageCommand{
		UserID:    c.UserID(),
		MessageID: p.MessageID,
		Mode:      mode,
	})
	if err != nil {
		h.ackChatError(c, frame.ID, err)
		return nil
	}
	c.ack(frame.ID, map[string]any{"status": "deleted", "result": res}, "")
	return nil
}

func (h *Handler) handleReaction(ctx context.Context, c *Connection, frame *domain.Frame, add bool) error {
	var p reactionPayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	cmd := application.ReactionCommand{
		UserID:    c.UserID(),
		MessageID: p.MessageID,
		Emoji:     p.Emoji,
	}
	var (
		res *application.ReactionResult
		err error
	)
	if add {
		res, err = h.chat.AddReaction(ctx, cmd)
	} else {
		res, err = h.chat.RemoveReaction(ctx, cmd)
	}
	if err != nil {
		h.ackChatError(c, frame.ID, err)
		return nil
	}
	c.ack(frame.ID, map[string]any{"status": "ok", "reaction": res}, "")
	return nil
}

func (h *Handler) handleReceiptRead(ctx context.Context, c *Connection, frame *domain.Frame) error {
	var p receiptPayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	_, err := h.chat.MarkRead(ctx, application.MarkReadCommand{
		UserID:         c.UserID(),
		ConversationID: p.ConversationID,
		ReadSeq:        p.LastReadSeq,
	})
	if err != nil {
		h.ackChatError(c, frame.ID, err)
		return nil
	}
	c.ack(frame.ID, map[string]any{"status": "ok"}, "")
	return nil
}

// handleReceiptDelivered is the §17.9 delivered cursor. v1 merges it into the
// read marker (the REST path has no separate delivered write); ack without a
// business call to keep the socket protocol faithful.
func (h *Handler) handleReceiptDelivered(ctx context.Context, c *Connection, frame *domain.Frame) error {
	var p receiptPayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	if p.LastReadSeq <= 0 {
		return errors.New("realtime: receipt.delivered missing last_delivered_seq")
	}
	c.ack(frame.ID, map[string]any{"status": "ok"}, "")
	return nil
}

// ackChatError maps an application error to the wire error vocabulary and acks
// the frame (API.md §18.3). Unknown errors are generic 500s; the socket stays
// open (§18.4).
func (h *Handler) ackChatError(c *Connection, id string, err error) {
	code := "INTERNAL"
	retryable := true
	if mapped, ok := chatErrorCode(err); ok {
		code = mapped.Code
		retryable = mapped.Retryable
	}
	h.ackError(c, id, wireError{Code: code, Detail: err.Error(), Retryable: retryable})
}

func (h *Handler) ackError(c *Connection, id string, we wireError) {
	c.ack(id, nil, we.Code)
}

// chatErrorCode maps chat application errors to the REST Appendix A codes.
func chatErrorCode(err error) (wireError, bool) {
	switch {
	case errors.Is(err, chatdomain.ErrNotMember):
		return wireError{Code: "NOT_A_MEMBER", Retryable: false}, true
	case errors.Is(err, chatdomain.ErrConversationNotFound):
		return wireError{Code: "CONVERSATION_NOT_FOUND", Retryable: true}, true
	case errors.Is(err, chatdomain.ErrMessageNotFound):
		return wireError{Code: "MESSAGE_NOT_FOUND", Retryable: false}, true
	case errors.Is(err, chatdomain.ErrEditWindowExpired):
		return wireError{Code: "EDIT_WINDOW_EXPIRED", Retryable: false}, true
	case errors.Is(err, chatdomain.ErrNotSender):
		return wireError{Code: "FORBIDDEN", Retryable: false}, true
	case errors.Is(err, chatdomain.ErrInsufficientRole):
		return wireError{Code: "INSUFFICIENT_ROLE", Retryable: false}, true
	default:
		return wireError{}, false
	}
}

// decodeData unmarshals a frame's data into a typed payload, rejecting garbage
// with a protocol error (the connection closes).
func decodeData(frame *domain.Frame, out any) error {
	if len(frame.Data) == 0 {
		return errors.New("realtime: frame missing data")
	}
	if err := json.Unmarshal(frame.Data, out); err != nil {
		return fmt.Errorf("realtime: invalid frame data: %w", err)
	}
	return nil
}
