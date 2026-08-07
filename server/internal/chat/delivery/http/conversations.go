package http

import (
	"net/http"
	"strings"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/apierr"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/httpapi"
)

// list handles GET /v1/conversations (§7.1): the chat list, most-recent-first,
// with filters, unread-only, and cursor pagination.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := httpapi.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, r, httpapi.ErrNoPrincipal())
		return
	}

	limit, err := httpapi.QueryLimit(r, 50, 100)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	if !validFilter(filter) {
		httpapi.WriteError(w, r, apierr.Validation("invalid query parameter", apierr.FieldError{Field: "filter", Reason: "invalid_filter"}))
		return
	}

	unreadOnly := r.URL.Query().Get("unread_only") == "true"

	res, err := h.svc.ListConversations(r.Context(), application.ListConversationsCommand{
		UserID:     p.UserID(),
		Filter:     filter,
		UnreadOnly: unreadOnly,
		Limit:      limit,
		Cursor:     r.URL.Query().Get("cursor"),
	})
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

func validFilter(f string) bool {
	switch f {
	case "all", "pinned", "archived", "groups", "direct":
		return true
	}
	return false
}

// create handles POST /v1/conversations (§7.2). A new direct/group conversation
// returns 201; an existing direct conversation with the same counterpart is
// returned with 200 (dedupe).
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, ok := httpapi.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, r, httpapi.ErrNoPrincipal())
		return
	}

	var body createConversationRequest
	if err := httpapi.DecodeJSON(r, &body); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	if body.DraftMessage != nil {
		httpapi.WriteError(w, r, apierr.Validation("draft_message is not supported yet",
			apierr.FieldError{Field: "draft_message", Reason: "not_supported"}))
		return
	}

	participants, err := httpapi.ParseIDs(body.ParticipantIDs)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	res, err := h.svc.CreateConversation(r.Context(), application.CreateConversationCommand{
		UserID:         p.UserID(),
		Type:           strings.ToLower(body.Type),
		ParticipantIDs: participants,
		Title:          body.Title,
		AvatarMediaID:  body.AvatarMediaID,
	})
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

// createConversationRequest is the §7.2 body. Ids arrive as strings on the wire
// (API.md §2.2) but are parsed to int64.
type createConversationRequest struct {
	Type           string    `json:"type"`
	ParticipantIDs []string  `json:"participant_ids"`
	Title          *string   `json:"title"`
	AvatarMediaID  *int64    `json:"avatar_media_id"`
	DraftMessage   *struct{} `json:"draft_message"` // deferred to M2 (422 handled at parse)
}

// get handles GET /v1/conversations/{conversation_id} (§7.3).
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
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

	res, err := h.svc.GetConversation(r.Context(), application.GetConversationCommand{
		UserID: p.UserID(), ConversationID: convID,
	})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

// update handles PATCH /v1/conversations/{conversation_id} (§7.4): owner/admin
// group-settings update.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
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

	var body updateConversationRequest
	if err := httpapi.DecodeJSON(r, &body); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	cmd := application.UpdateConversationCommand{UserID: p.UserID(), ConversationID: convID}
	if body.Title != nil {
		cmd.Title = body.Title
		cmd.TitleSet = true
	}
	if body.AvatarMediaID != nil {
		cmd.AvatarMediaID = body.AvatarMediaID
		cmd.AvatarSet = true
	}
	if body.Settings != nil {
		cmd.Settings = &application.SettingsPatch{
			SlowModeSeconds: body.Settings.SlowModeSeconds,
			AnyoneCanAdd:    body.Settings.AnyoneCanAdd,
			HistoryVisible:  body.Settings.HistoryVisible,
		}
	}

	res, err := h.svc.UpdateConversation(r.Context(), cmd)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

type updateConversationRequest struct {
	Title         *string        `json:"title"`
	AvatarMediaID *int64         `json:"avatar_media_id"`
	Settings      *settingsPatch `json:"settings"`
}

type settingsPatch struct {
	SlowModeSeconds *int    `json:"slow_mode_seconds"`
	AnyoneCanAdd    *bool   `json:"anyone_can_add"`
	HistoryVisible  *string `json:"history_visible"`
}
