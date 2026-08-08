package http

import (
	"net/http"
	"strconv"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/httpapi"
)

// markRead handles PUT /v1/conversations/{conversation_id}/receipts (§10.1,
// shared with §7.12): monotonic cursor advance via GREATEST.
func (h *Handler) markRead(w http.ResponseWriter, r *http.Request) {
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

	var body struct {
		LastReadSeq *string `json:"last_read_seq"`
		DeliverUpTo *string `json:"deliver_up_to_seq"`
	}
	if err := httpapi.DecodeJSON(r, &body); err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	if body.LastReadSeq == nil {
		httpapi.WriteError(w, r, invalidSeq("last_read_seq"))
		return
	}
	readSeq, err := parseSeq(*body.LastReadSeq)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}

	cmd := application.MarkReadCommand{
		UserID:         p.UserID(),
		ConversationID: convID,
		ReadSeq:        readSeq,
	}
	if body.DeliverUpTo != nil {
		delivered, perr := parseSeq(*body.DeliverUpTo)
		if perr != nil {
			httpapi.WriteError(w, r, perr)
			return
		}
		cmd.DeliveredSeq = &delivered
	}

	res, err := h.svc.MarkRead(r.Context(), cmd)
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

// getReceipts handles GET /v1/conversations/{conversation_id}/receipts (§10.2):
// per-member read state.
func (h *Handler) getReceipts(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.svc.GetReceipts(r.Context(), application.GetReceiptsCommand{
		UserID: p.UserID(), ConversationID: convID,
	})
	if err != nil {
		httpapi.WriteError(w, r, err)
		return
	}
	httpapi.OK(w, r, res)
}

// parseSeq parses a positive sequence string into an int64.
func parseSeq(raw string) (int64, error) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, invalidSeq("seq")
	}
	return n, nil
}
