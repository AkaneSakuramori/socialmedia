package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	chatdomain "github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// fakeService implements application.Service for handler round-trips.
type fakeService struct {
	createRes   *application.CreateConversationResult
	createErr   error
	createCmd   application.CreateConversationCommand
	createCalls atomic.Int64

	listRes *application.ConversationListResult
	listErr error
	listCmd application.ListConversationsCommand

	getRes *application.ConversationDetail
	getErr error
	getCmd application.GetConversationCommand

	updateRes *application.ConversationDetail
	updateErr error
	updateCmd application.UpdateConversationCommand

	listMembersRes *application.MemberListResult
	listMembersErr error
	listMembersCmd application.ListMembersCommand

	addRes *application.AddMembersResult
	addErr error
	addCmd application.AddMembersCommand

	removeErr error
	removeCmd application.RemoveMemberCommand

	roleRes *application.RoleChangeResult
	roleErr error
	roleCmd application.ChangeMemberRoleCommand

	muteRes *application.MuteResult
	muteErr error
	muteCmd application.SetMuteCommand

	pinRes *application.PinResult
	pinErr error
	pinCmd application.SetPinCommand

	archiveRes *application.ArchiveResult
	archiveErr error
	archiveCmd application.SetArchiveCommand
}

func (f *fakeService) CreateConversation(_ context.Context, cmd application.CreateConversationCommand) (*application.CreateConversationResult, error) {
	f.createCalls.Add(1)
	f.createCmd = cmd
	return f.createRes, f.createErr
}
func (f *fakeService) ListConversations(_ context.Context, cmd application.ListConversationsCommand) (*application.ConversationListResult, error) {
	f.listCmd = cmd
	return f.listRes, f.listErr
}
func (f *fakeService) GetConversation(_ context.Context, cmd application.GetConversationCommand) (*application.ConversationDetail, error) {
	f.getCmd = cmd
	return f.getRes, f.getErr
}
func (f *fakeService) UpdateConversation(_ context.Context, cmd application.UpdateConversationCommand) (*application.ConversationDetail, error) {
	f.updateCmd = cmd
	return f.updateRes, f.updateErr
}
func (f *fakeService) ListMembers(_ context.Context, cmd application.ListMembersCommand) (*application.MemberListResult, error) {
	f.listMembersCmd = cmd
	return f.listMembersRes, f.listMembersErr
}
func (f *fakeService) AddMembers(_ context.Context, cmd application.AddMembersCommand) (*application.AddMembersResult, error) {
	f.addCmd = cmd
	return f.addRes, f.addErr
}
func (f *fakeService) RemoveMember(_ context.Context, cmd application.RemoveMemberCommand) error {
	f.removeCmd = cmd
	return f.removeErr
}
func (f *fakeService) ChangeMemberRole(_ context.Context, cmd application.ChangeMemberRoleCommand) (*application.RoleChangeResult, error) {
	f.roleCmd = cmd
	return f.roleRes, f.roleErr
}
func (f *fakeService) SetMute(_ context.Context, cmd application.SetMuteCommand) (*application.MuteResult, error) {
	f.muteCmd = cmd
	return f.muteRes, f.muteErr
}
func (f *fakeService) SetPin(_ context.Context, cmd application.SetPinCommand) (*application.PinResult, error) {
	f.pinCmd = cmd
	return f.pinRes, f.pinErr
}
func (f *fakeService) SetArchive(_ context.Context, cmd application.SetArchiveCommand) (*application.ArchiveResult, error) {
	f.archiveCmd = cmd
	return f.archiveRes, f.archiveErr
}

// fakeAuth authenticates every token to user 42.
type fakeAuth struct{}

func (fakeAuth) Authenticate(_ context.Context, token, deviceID string) (*userdomain.User, error) {
	return &userdomain.User{ID: 42}, nil
}

// newTestRouter builds the full handler chain (auth + idempotency + routes)
// over a miniredis-backed client.
func newTestRouter(t *testing.T, svc application.Service) http.Handler {
	t.Helper()
	s := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	h := New(svc, fakeAuth{}, client)
	return h.Router()
}

// do sends an authenticated request. writes carry an Idempotency-Key.
func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer token-1")
	req.Header.Set("X-Device-Id", "dev-1")
	if method != http.MethodGet {
		req.Header.Set("Idempotency-Key", "req-"+method+path)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateConversationCreated(t *testing.T) {
	svc := &fakeService{
		createRes: &application.CreateConversationResult{
			View:    application.ConversationView{ID: "11", Type: "group"},
			Created: true,
		},
	}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodPost, "/v1/conversations",
		`{"type":"group","participant_ids":["5","6"],"title":"Squad"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if svc.createCmd.UserID != 42 {
		t.Errorf("cmd.UserID = %d, want 42", svc.createCmd.UserID)
	}
	if svc.createCmd.Type != "group" || svc.createCmd.Title == nil || *svc.createCmd.Title != "Squad" {
		t.Errorf("cmd = %+v, want group/Squad", svc.createCmd)
	}
	if len(svc.createCmd.ParticipantIDs) != 2 || svc.createCmd.ParticipantIDs[0] != 5 {
		t.Errorf("cmd.ParticipantIDs = %v, want [5 6]", svc.createCmd.ParticipantIDs)
	}
}

func TestCreateConversationDirectDedupeIs200(t *testing.T) {
	svc := &fakeService{
		createRes: &application.CreateConversationResult{View: application.ConversationView{ID: "1", Type: "direct"}},
	}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodPost, "/v1/conversations", `{"type":"direct","participant_ids":["9"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (dedupe)", rec.Code)
	}
}

func TestCreateConversationRejectsDraftMessage(t *testing.T) {
	h := newTestRouter(t, &fakeService{})
	rec := do(t, h, http.MethodPost, "/v1/conversations",
		`{"type":"direct","participant_ids":["9"],"draft_message":{"text":"hi"}}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

func TestCreateConversationRequiresIdempotencyKey(t *testing.T) {
	svc := &fakeService{
		createRes: &application.CreateConversationResult{View: application.ConversationView{ID: "1"}},
	}
	h := newTestRouter(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/conversations",
		bytes.NewReader([]byte(`{"type":"group","participant_ids":["5"]}`)))
	req.Header.Set("Authorization", "Bearer token-1")
	req.Header.Set("X-Device-Id", "dev-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (missing Idempotency-Key)", rec.Code)
	}
}

func TestListConversationsEnvelope(t *testing.T) {
	svc := &fakeService{
		listRes: &application.ConversationListResult{
			Items:   []application.ConversationView{{ID: "1", Type: "direct"}},
			HasMore: false,
			Limit:   50,
		},
	}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodGet, "/v1/conversations?filter=groups&limit=5", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.listCmd.Filter != "groups" || svc.listCmd.Limit != 5 || svc.listCmd.UserID != 42 {
		t.Errorf("cmd = %+v, want groups/5/42", svc.listCmd)
	}

	var env struct {
		Data       []json.RawMessage `json:"data"`
		Pagination struct {
			NextCursor *string `json:"next_cursor"`
			HasMore    bool    `json:"has_more"`
			Limit      int     `json:"limit"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if len(env.Data) != 1 {
		t.Errorf("data has %d items, want 1", len(env.Data))
	}
	if env.Pagination.HasMore || env.Pagination.Limit != 50 {
		t.Errorf("pagination = %+v, want has_more=false limit=50", env.Pagination)
	}
}

func TestListConversationsInvalidFilter(t *testing.T) {
	h := newTestRouter(t, &fakeService{})
	rec := do(t, h, http.MethodGet, "/v1/conversations?filter=bogus", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

func TestGetConversation(t *testing.T) {
	svc := &fakeService{
		getRes: &application.ConversationDetail{ID: "11", Type: "group"},
	}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodGet, "/v1/conversations/11", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.getCmd.ConversationID != 11 || svc.getCmd.UserID != 42 {
		t.Errorf("cmd = %+v, want conv 11 / user 42", svc.getCmd)
	}
}

func TestGetConversationInvalidPathID(t *testing.T) {
	h := newTestRouter(t, &fakeService{})
	rec := do(t, h, http.MethodGet, "/v1/conversations/abc", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

func TestUpdateConversation(t *testing.T) {
	svc := &fakeService{
		updateRes: &application.ConversationDetail{ID: "11"},
	}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodPatch, "/v1/conversations/11",
		`{"title":"Renamed","settings":{"anyone_can_add":true,"slow_mode_seconds":10}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.updateCmd.ConversationID != 11 || !svc.updateCmd.TitleSet || *svc.updateCmd.Title != "Renamed" {
		t.Errorf("cmd = %+v, want title Renamed", svc.updateCmd)
	}
	if svc.updateCmd.Settings == nil || svc.updateCmd.Settings.AnyoneCanAdd == nil || !*svc.updateCmd.Settings.AnyoneCanAdd {
		t.Errorf("settings patch missing anyone_can_add=true: %+v", svc.updateCmd.Settings)
	}
}

func TestListMembers(t *testing.T) {
	svc := &fakeService{
		listMembersRes: &application.MemberListResult{
			Items: []application.MemberView{{UserID: "42", Role: "owner", IsSelf: true}},
			Limit: 50,
		},
	}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodGet, "/v1/conversations/11/members?q=akane&limit=10", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.listMembersCmd.ConversationID != 11 || svc.listMembersCmd.Q != "akane" || svc.listMembersCmd.Limit != 10 {
		t.Errorf("cmd = %+v", svc.listMembersCmd)
	}
}

func TestAddMembers(t *testing.T) {
	svc := &fakeService{
		addRes: &application.AddMembersResult{
			Added:   []string{"5"},
			Skipped: []application.SkippedMember{{UserID: "6", Reason: "already_member"}},
		},
	}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodPost, "/v1/conversations/11/members", `{"user_ids":["5","6"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(svc.addCmd.UserIDs) != 2 || svc.addCmd.UserIDs[0] != 5 || svc.addCmd.ConversationID != 11 {
		t.Errorf("cmd = %+v", svc.addCmd)
	}
}

func TestRemoveMemberNoContent(t *testing.T) {
	svc := &fakeService{}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodDelete, "/v1/conversations/11/members/42", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if svc.removeCmd.ConversationID != 11 || svc.removeCmd.TargetUserID != 42 {
		t.Errorf("cmd = %+v", svc.removeCmd)
	}
}

func TestChangeMemberRole(t *testing.T) {
	svc := &fakeService{roleRes: &application.RoleChangeResult{UserID: "6", Role: "admin"}}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodPatch, "/v1/conversations/11/members/6", `{"role":"admin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.roleCmd.TargetUserID != 6 || svc.roleCmd.Role != "admin" {
		t.Errorf("cmd = %+v", svc.roleCmd)
	}
}

func TestSetMute(t *testing.T) {
	until := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := &fakeService{muteRes: &application.MuteResult{MutedUntil: &until}}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodPut, "/v1/conversations/11/mute", `{"until":"2030-01-01T00:00:00Z"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.muteCmd.ConversationID != 11 || svc.muteCmd.Until == nil || !svc.muteCmd.Until.Equal(until) {
		t.Errorf("cmd = %+v", svc.muteCmd)
	}
}

func TestSetPinAndArchive(t *testing.T) {
	svc := &fakeService{
		pinRes:     &application.PinResult{IsPinned: true},
		archiveRes: &application.ArchiveResult{IsArchived: true},
	}
	h := newTestRouter(t, svc)

	pinRec := do(t, h, http.MethodPut, "/v1/conversations/11/pin", `{"pinned":true}`)
	if pinRec.Code != http.StatusOK || svc.pinCmd.Pinned != true || svc.pinCmd.ConversationID != 11 {
		t.Errorf("pin: status=%d cmd=%+v", pinRec.Code, svc.pinCmd)
	}

	archRec := do(t, h, http.MethodPut, "/v1/conversations/11/archive", `{"archived":true}`)
	if archRec.Code != http.StatusOK || svc.archiveCmd.Archived != true {
		t.Errorf("archive: status=%d cmd=%+v", archRec.Code, svc.archiveCmd)
	}
}

func TestHandlerMapsDomainErrors(t *testing.T) {
	svc := &fakeService{getErr: chatdomain.ErrNotMember}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodGet, "/v1/conversations/11", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "NOT_A_MEMBER" {
		t.Errorf("code = %q, want NOT_A_MEMBER", body.Code)
	}
}

func TestHandlerRequiresAuth(t *testing.T) {
	h := newTestRouter(t, &fakeService{})
	req := httptest.NewRequest(http.MethodGet, "/v1/conversations", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestIdempotentCreateReplays(t *testing.T) {
	svc := &fakeService{
		createRes: &application.CreateConversationResult{View: application.ConversationView{ID: "11"}, Created: true},
	}
	h := newTestRouter(t, svc)

	doReq := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/conversations",
			strings.NewReader(`{"type":"direct","participant_ids":["9"]}`))
		req.Header.Set("Authorization", "Bearer token-1")
		req.Header.Set("X-Device-Id", "dev-1")
		req.Header.Set("Idempotency-Key", "unique-1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if doReq() != http.StatusCreated {
		t.Fatal("first create failed")
	}
	if doReq() != http.StatusCreated {
		t.Fatal("retry did not replay the cached 201")
	}
	if svc.createCalls.Load() != 1 {
		t.Errorf("CreateConversation called %d times, want 1 (replay)", svc.createCalls.Load())
	}
}

var _ application.Service = (*fakeService)(nil)
