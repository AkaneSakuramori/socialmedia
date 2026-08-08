package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	chatdomain "github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/presence"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/typing"
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

type typingPayload struct {
	ConversationID int64 `json:"conversation_id"`
}

type presenceUpdatePayload struct {
	Status       string `json:"status"`
	CustomStatus string `json:"custom_status"`
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

	// replay backs resume gap replay (API.md §16.6); nil → resume is always
	// rejected (v1 behavior).
	replay Replayer
	// head reports the change_log head for resume_ack's global_seq; best-effort.
	head HeadSource
	// presence/typing drive the ephemeral realtime state; nil-safe for tests
	// and for deployments where the services are not wired.
	presence *presence.Service
	typing   *typing.Service
}

// NewHandler builds the frame handler for the gateway.
func NewHandler(chat application.Service, log *slog.Logger) *Handler {
	return &Handler{chat: chat, log: log, now: time.Now}
}

// WithReplayer wires the resume replay source (shared with the dispatcher).
func (h *Handler) WithReplayer(r Replayer) *Handler {
	h.replay = r
	return h
}

// WithHeadSource wires the change_log head source for resume_ack cursors.
func (h *Handler) WithHeadSource(s HeadSource) *Handler {
	h.head = s
	return h
}

// WithPresence wires the ephemeral presence service (optional).
func (h *Handler) WithPresence(p *presence.Service) *Handler {
	h.presence = p
	return h
}

// WithTyping wires the ephemeral typing service (optional).
func (h *Handler) WithTyping(t *typing.Service) *Handler {
	h.typing = t
	return h
}

// HandleFrame dispatches one inbound frame (API.md §17). It returns an error
// only for protocol violations that should close the socket; business failures
// are acked with an error payload and keep the socket open (§18.4). It first
// enforces the per-connection WS frame budgets (§16.8, WS-3): a breach is acked
// RATE_LIMITED and, once sustained, closes the socket with 4501.
func (h *Handler) HandleFrame(ctx context.Context, c *Connection, frame *domain.Frame) error {
	// Rate-limit before dispatch. Ping/pong/ack are protocol frames and exempt
	// (they are bounded by the heartbeat and are not user work); everything else
	// counts toward the standard budget, with ephemeral classes throttled per
	// target.
	if class, key := rateClass(frame); class != "" {
		if !c.rate.allow(class, key) {
			h.ackError(c, frame.ID, wireError{Code: "RATE_LIMITED", Retryable: true})
			if c.rate.abusing() {
				h.log.Warn("realtime: closing socket for sustained rate-limit abuse",
					"conn", c.ID(), "user", c.UserID(), "class", class)
				return errors.New("realtime: rate-limit abuse")
			}
			return nil
		}
	}

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
		return h.handleUnsubscribe(ctx, c, frame)
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
	case domain.EventTypingStart:
		return h.handleTyping(ctx, c, frame, "typing")
	case domain.EventTypingStop:
		return h.handleTyping(ctx, c, frame, "stopped")
	case domain.EventPresenceUpdate:
		return h.handlePresenceUpdate(ctx, c, frame)
	case domain.EventAck:
		// Client ack of S2C events (API.md §17.13); the dispatcher owns cursor
		// advancement in the resume milestone.
		return nil
	default:
		return fmt.Errorf("realtime: unknown frame type %q", frame.Type)
	}
}

// handleResume is the reconnect continuation (API.md §16.6/§17.2). It
// validates the payload and the bound session, then replays the gap from the
// dispatcher's replay buffer when the cursor is inside the replay window —
// otherwise it rejects (resume_rejected) so the client falls back to full
// resynchronization (snapshot + delta bootstrap, API.md §16.3).
func (h *Handler) handleResume(ctx context.Context, c *Connection, frame *domain.Frame) error {
	var p resumePayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	if p.LastSeq < 0 || p.LastGlobalSeq < 0 {
		return errors.New("realtime: resume cursors must be non-negative")
	}
	// Resume must continue the session the socket is bound to. A claimed
	// session mismatch is a protocol/security violation: reject, never replay
	// into the wrong session.
	if p.SessionID != 0 && p.SessionID != c.SessionID() {
		h.log.Warn("realtime: resume session mismatch",
			"conn", c.ID(), "user", c.UserID(), "claimed", p.SessionID, "bound", c.SessionID())
		c.sendEvent(domain.EventResumeRejected, map[string]any{"reason": "session_revoked"})
		return nil
	}
	// The replay window must cover the whole gap; otherwise the client must
	// resynchronize from the durable store (API.md §16.6: buffer TTL exceeded
	// or cursor stale → resume_rejected).
	if h.replay == nil || !h.replay.CanReplay(p.LastGlobalSeq) {
		c.sendEvent(domain.EventResumeRejected, map[string]any{"reason": "buffer_expired"})
		return nil
	}
	replay := h.replay.ReplaySince(p.LastGlobalSeq)
	c.sendResumeAck(p.LastSeq, h.headGlobalSeq(ctx), replay)
	return nil
}

// headGlobalSeq resolves the change_log head for resume_ack (best-effort; 0
// when unavailable — the client reconciles with its own acked cursor).
func (h *Handler) headGlobalSeq(ctx context.Context) int64 {
	if h.head == nil {
		return 0
	}
	if v, err := h.head.Head(ctx); err == nil {
		return v
	} else {
		h.log.Debug("realtime: resume head unavailable", "error", err)
	}
	return 0
}

// handleTyping drives a typing.start / typing.stop (API.md §17.10). Membership
// is re-verified so a non-member cannot inject typing indicators into a
// conversation (WS-4 parity); the typing service throttles broadcasts per
// (user, conversation) and expires state. Ephemeral — never persisted,
// never replayed.
func (h *Handler) handleTyping(ctx context.Context, c *Connection, frame *domain.Frame, status string) error {
	var p typingPayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	if p.ConversationID <= 0 {
		return errors.New("realtime: typing missing conversation_id")
	}
	if h.typing == nil {
		c.ack(frame.ID, map[string]any{"status": "ok"}, "")
		return nil
	}
	// Only members may signal typing (bounded by the per-conversation frame
	// budget, so the membership check cannot be turned into a read storm).
	if _, err := h.chat.GetConversation(ctx, application.GetConversationCommand{
		UserID:         c.UserID(),
		ConversationID: p.ConversationID,
	}); err != nil {
		h.ackChatError(c, frame.ID, err)
		return nil
	}
	ectx, cancel := h.ephemeralCtx(ctx)
	defer cancel()
	if status == "stopped" {
		h.typing.Stop(ectx, c.UserID(), p.ConversationID)
	} else {
		h.typing.Start(ectx, c.UserID(), p.ConversationID)
	}
	c.ack(frame.ID, map[string]any{"status": "ok"}, "")
	return nil
}

// handlePresenceUpdate applies a client presence.update (API.md §17.11). The
// status vocabulary is enforced; last-seen is never client-claimed — it is
// derived server-side from connection lifecycle.
func (h *Handler) handlePresenceUpdate(ctx context.Context, c *Connection, frame *domain.Frame) error {
	var p presenceUpdatePayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	if p.Status == "" {
		p.Status = "online"
	}
	if !validPresenceStatus(p.Status) {
		h.ackError(c, frame.ID, wireError{Code: "VALIDATION_ERROR", Detail: "status must be online|offline|away|busy", Retryable: false})
		return nil
	}
	if h.presence != nil {
		ectx, cancel := h.ephemeralCtx(ctx)
		defer cancel()
		h.presence.Update(ectx, c.UserID(), p.Status, p.CustomStatus)
	}
	c.ack(frame.ID, map[string]any{"status": "ok"}, "")
	return nil
}

// ephemeralCtx bounds presence/typing Redis calls so a degraded Redis cannot
// block a connection's read pump indefinitely (Redis failure handling).
func (h *Handler) ephemeralCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 2*time.Second)
}

func validPresenceStatus(s string) bool {
	switch s {
	case "online", "offline", "away", "busy":
		return true
	default:
		return false
	}
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
		if h.presence != nil {
			ectx, cancel := h.ephemeralCtx(ctx)
			h.presence.SetConversation(ectx, c.UserID(), convID)
			cancel()
		}
	}
	c.ack(frame.ID, map[string]any{"subscribed": subscribed}, "")
	return nil
}

// handleUnsubscribe leaves a conversation's fan-out (API.md §17.3). Unsubscribing
// from a conversation never subscribed is a no-op success.
func (h *Handler) handleUnsubscribe(ctx context.Context, c *Connection, frame *domain.Frame) error {
	var p unsubscribePayload
	if err := decodeData(frame, &p); err != nil {
		return err
	}
	c.Unsubscribe(p.ConversationID)
	if h.presence != nil {
		ectx, cancel := h.ephemeralCtx(ctx)
		h.presence.DropConversation(ectx, c.UserID(), p.ConversationID)
		cancel()
	}
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

// rateClass maps a frame to its rate-limit tier and bucket key (API.md §16.8,
// Appendix B). Protocol frames (ping/pong/ack) return "" → exempt. Ephemeral
// classes are keyed by target so each conversation/user gets its own budget;
// durable and C2S frames fall to the per-connection standard budget.
func rateClass(frame *domain.Frame) (class, key string) {
	switch frame.Type {
	case domain.EventPing, domain.EventPong, domain.EventAck:
		return "", ""
	case domain.EventTypingStart:
		// ws_typing: 1 per 2 s per conversation. typing.stop is exempt: a stop
		// is a corrective state-clear that must always land so indicators never
		// stay stuck on (API.md §17.10: stop on send/blur, auto-expire covers a
		// missed stop only as a fallback).
		var p struct {
			ConversationID int64 `json:"conversation_id"`
		}
		_ = json.Unmarshal(frame.Data, &p)
		return "typing", strconv.FormatInt(p.ConversationID, 10)
	case domain.EventTypingStop:
		return "", ""
	case domain.EventPresenceUpdate:
		return "presence", "" // 1 per s per user
	case domain.EventReceiptRead, domain.EventReceiptDelivered:
		// ws_read: 1 per 500 ms per conversation.
		var p struct {
			ConversationID int64 `json:"conversation_id"`
		}
		_ = json.Unmarshal(frame.Data, &p)
		return "read", strconv.FormatInt(p.ConversationID, 10)
	default:
		// Everything else (message.*, reaction.*, subscribe, resume, hello)
		// counts toward the per-connection standard budget.
		return "standard", ""
	}
}
