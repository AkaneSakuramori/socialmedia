// Package http is the chat module's REST delivery layer (API.md §7). Handlers
// are thin: they decode/validate request shapes, call the application service,
// and let httpapi serialize responses and map errors. No business logic lives
// here (ENGINEERING.md §8, §10).
package http

import (
	"net/http"

	"github.com/redis/go-redis/v9"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/httpapi"
)

// Handler mounts the conversation API (API.md §7.1–§7.11). svc is the chat
// application service; auth validates access tokens at the gateway; redis backs
// the Idempotency-Key cache (API.md §2.7).
type Handler struct {
	svc   application.Service
	auth  httpapi.Authenticator
	redis *redis.Client
}

// New builds the conversation handler set.
func New(svc application.Service, auth httpapi.Authenticator, redis *redis.Client) *Handler {
	return &Handler{svc: svc, auth: auth, redis: redis}
}

// Router returns the mounted routes with the platform middleware chain applied:
// bearer auth on every route, and the Idempotency-Key gate on unsafe writes.
func (h *Handler) Router() http.Handler {
	mux := http.NewServeMux()

	// Safe reads.
	mux.Handle("GET /v1/conversations", http.HandlerFunc(h.list))
	mux.Handle("GET /v1/conversations/{conversation_id}", http.HandlerFunc(h.get))
	mux.Handle("GET /v1/conversations/{conversation_id}/members", http.HandlerFunc(h.listMembers))
	mux.Handle("GET /v1/conversations/{conversation_id}/messages", http.HandlerFunc(h.listMessages))
	mux.Handle("GET /v1/conversations/{conversation_id}/receipts", http.HandlerFunc(h.getReceipts))
	mux.Handle("GET /v1/messages/{message_id}", http.HandlerFunc(h.getMessage))
	mux.Handle("GET /v1/messages/{message_id}/reactions", http.HandlerFunc(h.listReactions))

	// Unsafe writes — Idempotency-Key required.
	unsafe := httpapi.Idempotency(h.redis)
	mux.Handle("POST /v1/conversations", unsafe(http.HandlerFunc(h.create)))
	mux.Handle("PATCH /v1/conversations/{conversation_id}", unsafe(http.HandlerFunc(h.update)))
	mux.Handle("POST /v1/conversations/{conversation_id}/members", unsafe(http.HandlerFunc(h.addMembers)))
	mux.Handle("DELETE /v1/conversations/{conversation_id}/members/{user_id}", unsafe(http.HandlerFunc(h.removeMember)))
	mux.Handle("PATCH /v1/conversations/{conversation_id}/members/{user_id}", unsafe(http.HandlerFunc(h.changeMemberRole)))
	mux.Handle("PUT /v1/conversations/{conversation_id}/mute", unsafe(http.HandlerFunc(h.setMute)))
	mux.Handle("PUT /v1/conversations/{conversation_id}/pin", unsafe(http.HandlerFunc(h.setPin)))
	mux.Handle("PUT /v1/conversations/{conversation_id}/archive", unsafe(http.HandlerFunc(h.setArchive)))
	mux.Handle("PUT /v1/conversations/{conversation_id}/receipts", unsafe(http.HandlerFunc(h.markRead)))
	mux.Handle("POST /v1/conversations/{conversation_id}/messages", unsafe(http.HandlerFunc(h.sendMessage)))
	mux.Handle("PATCH /v1/messages/{message_id}", unsafe(http.HandlerFunc(h.editMessage)))
	mux.Handle("DELETE /v1/messages/{message_id}", unsafe(http.HandlerFunc(h.deleteMessage)))
	mux.Handle("PUT /v1/messages/{message_id}/reactions/{emoji}", unsafe(http.HandlerFunc(h.addReaction)))
	mux.Handle("DELETE /v1/messages/{message_id}/reactions/{emoji}", unsafe(http.HandlerFunc(h.removeReaction)))

	return httpapi.RequireAuth(h.auth)(mux)
}
