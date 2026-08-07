package http

import (
	"net/http"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/httpapi"
)

// listMembers handles GET /v1/conversations/{conversation_id}/members (§7.5).
func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
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

	res, err := h.svc.ListMembers(r.Context(), application.ListMembersCommand{
		UserID:         p.UserID(),
		ConversationID: convID,
		Limit:          limit,
		Cursor:         r.URL.Query().Get("cursor"),
		Q:              r.URL.Query().Get("q"),
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

// addMembers handles POST /v1/conversations/{conversation_id}/members (§7.6).
// Partial success is 200 with the added/skipped breakdown.
func (h *Handler) addMembers(w http.ResponseWriter, r *http.Request) {
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

	var body addMembersRequest
	if err := httpapi.DecodeJSON(r, &body); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	userIDs, err := httpapi.ParseIDs(body.UserIDs)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	res, err := h.svc.AddMembers(r.Context(), application.AddMembersCommand{
		UserID:         p.UserID(),
		ConversationID: convID,
		UserIDs:        userIDs,
	})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

type addMembersRequest struct {
	UserIDs []string `json:"user_ids"`
}

// removeMember handles DELETE /v1/conversations/{conversation_id}/members/
// {user_id} (§7.7): self-leave or admin-removal. Responds 204.
func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request) {
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
	target, err := httpapi.PathID(r, "user_id")
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	if err := h.svc.RemoveMember(r.Context(), application.RemoveMemberCommand{
		UserID:         p.UserID(),
		ConversationID: convID,
		TargetUserID:   target,
	}); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.NoContent(w, r)
}

// changeMemberRole handles PATCH /v1/conversations/{conversation_id}/members/
// {user_id} (§7.8).
func (h *Handler) changeMemberRole(w http.ResponseWriter, r *http.Request) {
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
	target, err := httpapi.PathID(r, "user_id")
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	var body roleRequest
	if err := httpapi.DecodeJSON(r, &body); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	res, err := h.svc.ChangeMemberRole(r.Context(), application.ChangeMemberRoleCommand{
		UserID:         p.UserID(),
		ConversationID: convID,
		TargetUserID:   target,
		Role:           body.Role,
	})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

type roleRequest struct {
	Role string `json:"role"`
}
