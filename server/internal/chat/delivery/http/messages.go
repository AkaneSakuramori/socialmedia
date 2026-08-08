package http

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/apierr"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/httpapi"
)

// listMessages handles GET /v1/conversations/{conversation_id}/messages (§8.1):
// keyset-paginated history (before=<seq>) or sync delta (after_global_seq=n),
// strictly ascending within the page.
func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request) {
	p, ok := httpapi.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, r, httpapi.ErrNoPrincipal())
		return
	}
	convID, err := httpapi.PathID(r, "conversation_id")
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	limit, err := httpapi.QueryLimit(r, 50, 100)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	cmd := application.ListMessagesCommand{
		UserID:         p.UserID(),
		ConversationID: convID,
		Limit:          limit,
		Cursor:         r.URL.Query().Get("cursor"),
	}
	if cmd.BeforeSeq, err = querySeq(r, "before"); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	if cmd.AfterGlobalSeq, err = querySeq(r, "after_global_seq"); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	res, err := h.svc.ListMessages(r.Context(), cmd)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	items := make([]any, len(res.Items))
	for i := range res.Items {
		items[i] = res.Items[i]
	}
	httpapi.List(w, r, items, res.Next, res.HasMore, res.Limit)
}

// sendMessage handles POST /v1/conversations/{conversation_id}/messages (§8.2).
// A first send returns 201; an idempotent replay of the same client_msg_id
// returns 200 with the original message.
func (h *Handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	p, ok := httpapi.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, r, httpapi.ErrNoPrincipal())
		return
	}
	convID, err := httpapi.PathID(r, "conversation_id")
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	var body sendMessageRequest
	if err := httpapi.DecodeJSON(r, &body); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	cmd := application.SendMessageCommand{
		UserID:         p.UserID(),
		ConversationID: convID,
		ClientMsgID:    body.ClientMsgID,
		Type:           strings.ToLower(body.Type),
		Text:           body.ContentText(),
		Media:          body.Media,
		Mentions:       body.MentionIDs(),
	}
	if body.ReplyToSeq != nil {
		seq, perr := strconv.ParseInt(*body.ReplyToSeq, 10, 64)
		if perr != nil || seq <= 0 {
			httpapi.WriteError(w, r, invalidSeq("reply_to_seq"))
			return
		}
		cmd.ReplyToSeq = &seq
	}

	res, err := h.svc.SendMessage(r.Context(), cmd)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	if res.Created {
		httpapi.Created(w, r, res.View)
		return
	}
	httpapi.OK(w, r, res.View)
}

// sendMessageRequest is the §8.2 body.
type sendMessageRequest struct {
	ClientMsgID string              `json:"client_msg_id"`
	Type        string              `json:"type"`
	Content     *messageContentBody `json:"content"`
	Media       []domain.Attachment `json:"media"`
	ReplyToSeq  *string             `json:"reply_to_seq"`
	Mentions    []string            `json:"mentions"`
}

type messageContentBody struct {
	Text *string `json:"text"`
}

// ContentText returns the text body, or nil when the client sent media-only.
func (b *sendMessageRequest) ContentText() *string {
	if b.Content != nil {
		return b.Content.Text
	}
	return nil
}

// MentionIDs parses the §8.2 mentions[] into int64s (ids arrive as strings).
func (b *sendMessageRequest) MentionIDs() []int64 {
	ids := make([]int64, 0, len(b.Mentions))
	for _, raw := range b.Mentions {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// getMessage handles GET /v1/messages/{message_id} (§8.3).
func (h *Handler) getMessage(w http.ResponseWriter, r *http.Request) {
	p, ok := httpapi.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, r, httpapi.ErrNoPrincipal())
		return
	}
	msgID, err := httpapi.PathID(r, "message_id")
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	res, err := h.svc.GetMessage(r.Context(), application.GetMessageCommand{
		UserID: p.UserID(), MessageID: msgID,
	})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

// editMessage handles PATCH /v1/messages/{message_id} (§8.4): sender-only,
// within the edit window, content only.
func (h *Handler) editMessage(w http.ResponseWriter, r *http.Request) {
	p, ok := httpapi.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, r, httpapi.ErrNoPrincipal())
		return
	}
	msgID, err := httpapi.PathID(r, "message_id")
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	var body editMessageRequest
	if err := httpapi.DecodeJSON(r, &body); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	res, err := h.svc.EditMessage(r.Context(), application.EditMessageCommand{
		UserID: p.UserID(), MessageID: msgID, NewText: body.NewText(),
	})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

type editMessageRequest struct {
	Content *messageContentBody `json:"content"`
}

// NewText returns the edit body, or "" when the client omitted content (the
// application layer then reports the field validation error).
func (b *editMessageRequest) NewText() string {
	if b.Content != nil && b.Content.Text != nil {
		return *b.Content.Text
	}
	return ""
}

// deleteMessage handles DELETE /v1/messages/{message_id} (§8.5): mode=all
// (tombstone, default) or mode=self (client-local no-op).
func (h *Handler) deleteMessage(w http.ResponseWriter, r *http.Request) {
	p, ok := httpapi.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, r, httpapi.ErrNoPrincipal())
		return
	}
	msgID, err := httpapi.PathID(r, "message_id")
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	var body struct {
		Mode string `json:"mode"`
	}
	httpapi.DecodeJSON(r, &body) // empty body (no mode) is valid → default "all"

	res, err := h.svc.DeleteMessage(r.Context(), application.DeleteMessageCommand{
		UserID: p.UserID(), MessageID: msgID, Mode: body.Mode,
	})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

// addReaction handles PUT /v1/messages/{message_id}/reactions/{emoji} (§8.6).
// The emoji path segment is URL-decoded by net/http.
func (h *Handler) addReaction(w http.ResponseWriter, r *http.Request) {
	h.react(w, r, true)
}

// removeReaction handles DELETE /v1/messages/{message_id}/reactions/{emoji}
// (§8.7).
func (h *Handler) removeReaction(w http.ResponseWriter, r *http.Request) {
	h.react(w, r, false)
}

func (h *Handler) react(w http.ResponseWriter, r *http.Request, add bool) {
	p, ok := httpapi.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, r, httpapi.ErrNoPrincipal())
		return
	}
	msgID, err := httpapi.PathID(r, "message_id")
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	emoji := r.PathValue("emoji")

	cmd := application.ReactionCommand{UserID: p.UserID(), MessageID: msgID, Emoji: emoji}
	var res *application.ReactionResult
	if add {
		res, err = h.svc.AddReaction(r.Context(), cmd)
	} else {
		res, err = h.svc.RemoveReaction(r.Context(), cmd)
	}
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

// listReactions handles GET /v1/messages/{message_id}/reactions (§8.8).
func (h *Handler) listReactions(w http.ResponseWriter, r *http.Request) {
	p, ok := httpapi.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, r, httpapi.ErrNoPrincipal())
		return
	}
	msgID, err := httpapi.PathID(r, "message_id")
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	emoji := r.URL.Query().Get("emoji")

	res, err := h.svc.ListReactions(r.Context(), application.ListReactionsCommand{
		UserID: p.UserID(), MessageID: msgID, Emoji: emoji,
	})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

// querySeq parses an optional positive int64 query parameter; missing → nil.
func querySeq(r *http.Request, name string) (*int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return nil, invalidSeq(name)
	}
	return &n, nil
}

// invalidSeq classifies a malformed sequence reference as a field error.
func invalidSeq(field string) error {
	return apierr.Validation("invalid sequence",
		apierr.FieldError{Field: field, Reason: "invalid_seq"})
}
