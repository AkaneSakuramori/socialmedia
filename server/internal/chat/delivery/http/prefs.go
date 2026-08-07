package http

import (
	"net/http"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/httpapi"
)

// setMute handles PUT /v1/conversations/{conversation_id}/mute (§7.9).
func (h *Handler) setMute(w http.ResponseWriter, r *http.Request) {
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

	var body muteRequest
	if err := httpapi.DecodeJSON(r, &body); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	res, err := h.svc.SetMute(r.Context(), application.SetMuteCommand{
		UserID:         p.UserID(),
		ConversationID: convID,
		Until:          body.Until,
	})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

// muteRequest carries the mute deadline (RFC 3339) or null to unmute.
type muteRequest struct {
	Until *time.Time `json:"until"`
}

// setPin handles PUT /v1/conversations/{conversation_id}/pin (§7.10).
func (h *Handler) setPin(w http.ResponseWriter, r *http.Request) {
	h.setFlag(w, r, "pin")
}

// setArchive handles PUT /v1/conversations/{conversation_id}/archive (§7.11).
func (h *Handler) setArchive(w http.ResponseWriter, r *http.Request) {
	h.setFlag(w, r, "archive")
}

// setFlag is the shared implementation for pin/archive toggles.
func (h *Handler) setFlag(w http.ResponseWriter, r *http.Request, kind string) {
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

	if kind == "pin" {
		var body pinRequest
		if err := httpapi.DecodeJSON(r, &body); err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		res, err := h.svc.SetPin(r.Context(), application.SetPinCommand{
			UserID: p.UserID(), ConversationID: convID, Pinned: body.Pinned,
		})
		if err != nil {
			httpapi.WriteError(w, r, err)
			return
		}
		httpapi.OK(w, r, res)
		return
	}

	var body archiveRequest
	if err := httpapi.DecodeJSON(r, &body); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	res, err := h.svc.SetArchive(r.Context(), application.SetArchiveCommand{
		UserID: p.UserID(), ConversationID: convID, Archived: body.Archived,
	})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

// pinRequest is the §7.10 body.
type pinRequest struct {
	Pinned bool `json:"pinned"`
}

// archiveRequest is the §7.11 body.
type archiveRequest struct {
	Archived bool `json:"archived"`
}
