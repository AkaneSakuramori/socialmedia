package application

import (
	"errors"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// seedGroup wires a group with the given roles: owner, then a variadic list of
// (userID, role) pairs.
func (h *harness) seedGroup(convID, ownerID int64, members ...[2]any) *domain.Conversation {
	h.users.seed(ownerID, "Owner")
	c := h.seedConversation(convID, ownerID, domain.ConversationGroup, strptr("Trip"), nil)
	now := h.now
	for _, p := range members {
		uid := p[0].(int64)
		role := p[1].(domain.Role)
		h.users.seed(uid, nameFor(uid))
		ms := newMembership(convID, uid, role, now)
		h.members.rows[convID][uid] = ms
	}
	return c
}

func nameFor(id int64) string {
	switch id {
	case 1001:
		return "Aya"
	case 1002:
		return "Sami"
	case 1003:
		return "Ravi"
	default:
		return "User"
	}
}

// ---- GET /v1/conversations/{id}/members (§7.5) ----

func TestListMembersPaginationAndSearch(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001, [2]any{int64(1002), domain.RoleMember}, [2]any{int64(1003), domain.RoleAdmin})

	res, err := h.svc.ListMembers(t.Context(), ListMembersCommand{UserID: 1001, ConversationID: 1, Limit: 2})
	if err != nil {
		t.Fatalf("ListMembers error: %v", err)
	}
	if len(res.Items) != 2 || !res.HasMore {
		t.Errorf("page = %d items (has_more=%v), want 2/true (3 members, page size 2)", len(res.Items), res.HasMore)
	}
	// Ordered by joined_at DESC → newest first (1003, then 1002, then 1001).
	if res.Items[0].UserID != "1003" {
		t.Errorf("first = %s, want 1003 (newest join)", res.Items[0].UserID)
	}
	if !res.Items[1].IsSelf && res.Items[1].UserID != "1002" {
		t.Errorf("second = %s, want 1002", res.Items[1].UserID)
	}

	// Pagination completes the page; the owner is last.
	res2, err := h.svc.ListMembers(t.Context(), ListMembersCommand{UserID: 1001, ConversationID: 1, Limit: 2, Cursor: *res.Next})
	if err != nil {
		t.Fatalf("ListMembers page 2 error: %v", err)
	}
	if len(res2.Items) != 1 || res2.Items[0].UserID != "1001" || !res2.Items[0].IsSelf {
		t.Errorf("page2 = %+v, want owner self row", res2.Items)
	}
	if res2.HasMore || res2.Next != nil {
		t.Errorf("page2 has_more=%v next=%v, want false/nil", res2.HasMore, res2.Next)
	}

	// q filter on display name (case-insensitive substring).
	resq, err := h.svc.ListMembers(t.Context(), ListMembersCommand{UserID: 1001, ConversationID: 1, Q: "sam"})
	if err != nil {
		t.Fatalf("ListMembers q error: %v", err)
	}
	if len(resq.Items) != 1 || resq.Items[0].UserID != "1002" {
		t.Errorf("q=sam → %+v, want [1002]", resq.Items)
	}
}

func TestListMembersRequiresMembership(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001)
	_, err := h.svc.ListMembers(t.Context(), ListMembersCommand{UserID: 9999, ConversationID: 1})
	if !errors.Is(err, domain.ErrNotMember) {
		t.Errorf("err = %v, want ErrNotMember", err)
	}
}

// ---- POST /v1/conversations/{id}/members (§7.6) ----

func TestAddMembersPartialSuccess(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001, [2]any{int64(1002), domain.RoleAdmin})
	h.users.seed(1003, "Ravi") // not yet a member
	h.users.seed(1004, "Noor")
	// 1005 does not exist; 1002 is already a member.

	res, err := h.svc.AddMembers(t.Context(), AddMembersCommand{
		UserID: 1002, ConversationID: 1, UserIDs: []int64{1003, 1004, 1005, 1002},
	})
	if err != nil {
		t.Fatalf("AddMembers error: %v", err)
	}
	if len(res.Added) != 2 || res.Added[0] != "1003" || res.Added[1] != "1004" {
		t.Errorf("added = %v, want [1003 1004]", res.Added)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("skipped = %v, want 2 entries", res.Skipped)
	}
	// Order-preserving: 1005 unknown, 1002 already a member.
	if res.Skipped[0].UserID != "1005" || res.Skipped[0].Reason != "unknown_user" {
		t.Errorf("skipped[0] = %+v, want 1005/unknown_user", res.Skipped[0])
	}
	if res.Skipped[1].UserID != "1002" || res.Skipped[1].Reason != "already_member" {
		t.Errorf("skipped[1] = %+v, want 1002/already_member", res.Skipped[1])
	}
	if n, _ := h.members.CountActive(t.Context(), 1); n != 4 {
		t.Errorf("active members = %d, want 4", n)
	}
}

func TestAddMembersMemberRequiresAnyoneCanAdd(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001, [2]any{int64(1002), domain.RoleMember})
	h.users.seed(1003, "Ravi")

	_, err := h.svc.AddMembers(t.Context(), AddMembersCommand{UserID: 1002, ConversationID: 1, UserIDs: []int64{1003}})
	if !errors.Is(err, domain.ErrInsufficientRole) {
		t.Errorf("err = %v, want ErrInsufficientRole", err)
	}

	// anyone_can_add lifts the gate for members.
	c := h.convos.byID[1]
	s := c.Settings
	s.AnyoneCanAdd = true
	c.Settings = s
	res, err := h.svc.AddMembers(t.Context(), AddMembersCommand{UserID: 1002, ConversationID: 1, UserIDs: []int64{1003}})
	if err != nil {
		t.Fatalf("AddMembers with anyone_can_add error: %v", err)
	}
	if len(res.Added) != 1 || res.Added[0] != "1003" {
		t.Errorf("added = %v, want [1003]", res.Added)
	}
}

func TestAddMembersDuplicateInputValidation(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001, [2]any{int64(1002), domain.RoleAdmin})
	_, err := h.svc.AddMembers(t.Context(), AddMembersCommand{
		UserID: 1002, ConversationID: 1, UserIDs: []int64{1003, 1003},
	})
	ve, ok := err.(*domain.ValidationError)
	if !ok || ve.Field != "user_ids" {
		t.Errorf("err = %v, want user_ids validation", err)
	}
}

func TestAddMembersConversationFull(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001, [2]any{int64(1002), domain.RoleAdmin})
	// Fill the group to the 500 cap minus 1 with a member add, then exceed it.
	var ids []int64
	for i := int64(3); i <= 500; i++ {
		h.users.seed(i, nameFor(i))
		ids = append(ids, i)
	}
	h.members.rows[1][1002] = &domain.Membership{ConversationID: 1, UserID: 1002, Role: domain.RoleAdmin, JoinedAt: h.now}

	res, err := h.svc.AddMembers(t.Context(), AddMembersCommand{UserID: 1002, ConversationID: 1, UserIDs: ids})
	if err != nil {
		t.Fatalf("fill to cap error: %v", err)
	}
	if len(res.Added) != 498 {
		t.Fatalf("added = %d, want 498", len(res.Added))
	}

	// One more would exceed 500 total.
	h.users.seed(600, "Xtra")
	_, err = h.svc.AddMembers(t.Context(), AddMembersCommand{UserID: 1002, ConversationID: 1, UserIDs: []int64{600}})
	if !errors.Is(err, domain.ErrConversationFull) {
		t.Errorf("err = %v, want ErrConversationFull", err)
	}
}

// ---- DELETE /v1/conversations/{id}/members/{user_id} (§7.7) ----

func TestRemoveMemberSelfLeave(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001, [2]any{int64(1002), domain.RoleMember})

	if err := h.svc.RemoveMember(t.Context(), RemoveMemberCommand{UserID: 1002, ConversationID: 1, TargetUserID: 1002}); err != nil {
		t.Fatalf("self-leave error: %v", err)
	}
	if _, err := h.members.FindActive(t.Context(), 1, 1002); !errors.Is(err, domain.ErrMembershipNotFound) {
		t.Error("leaver must no longer be an active member")
	}
	// Conversation survives while members remain.
	if _, err := h.svc.GetConversation(t.Context(), GetConversationCommand{UserID: 1001, ConversationID: 1}); err != nil {
		t.Errorf("conversation must survive a single leave: %v", err)
	}
}

func TestRemoveMemberRequiresRole(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001, [2]any{int64(1002), domain.RoleMember}, [2]any{int64(1003), domain.RoleAdmin})

	// member cannot remove another member.
	if err := h.svc.RemoveMember(t.Context(), RemoveMemberCommand{UserID: 1002, ConversationID: 1, TargetUserID: 1003}); !errors.Is(err, domain.ErrInsufficientRole) {
		t.Errorf("member removing admin err = %v, want ErrInsufficientRole", err)
	}
	// admin cannot remove another admin (owner only).
	if err := h.svc.RemoveMember(t.Context(), RemoveMemberCommand{UserID: 1003, ConversationID: 1, TargetUserID: 1001}); !errors.Is(err, domain.ErrCannotRemoveOwner) {
		t.Errorf("removing owner err = %v, want ErrCannotRemoveOwner", err)
	}
	// owner can remove an admin.
	if err := h.svc.RemoveMember(t.Context(), RemoveMemberCommand{UserID: 1001, ConversationID: 1, TargetUserID: 1003}); err != nil {
		t.Errorf("owner removing admin error: %v", err)
	}
}

func TestRemoveLastMemberTombstonesConversation(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001, [2]any{int64(1002), domain.RoleMember})

	// Self-leave then owner-leave → last member leaves → tombstoned.
	if err := h.svc.RemoveMember(t.Context(), RemoveMemberCommand{UserID: 1002, ConversationID: 1, TargetUserID: 1002}); err != nil {
		t.Fatalf("first leave: %v", err)
	}
	if err := h.svc.RemoveMember(t.Context(), RemoveMemberCommand{UserID: 1001, ConversationID: 1, TargetUserID: 1001}); err != nil {
		t.Fatalf("owner leave: %v", err)
	}
	if _, err := h.svc.GetConversation(t.Context(), GetConversationCommand{UserID: 1001, ConversationID: 1}); !errors.Is(err, domain.ErrConversationNotFound) {
		t.Error("tombstoned conversation must not be found")
	}
}

// ---- PATCH /v1/conversations/{id}/members/{user_id} (§7.8) ----

func TestChangeRoleOwnerOnly(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001, [2]any{int64(1002), domain.RoleAdmin}, [2]any{int64(1003), domain.RoleMember})

	// owner grants owner to an admin.
	if _, err := h.svc.ChangeMemberRole(t.Context(), ChangeMemberRoleCommand{UserID: 1001, ConversationID: 1, TargetUserID: 1002, Role: "owner"}); err != nil {
		t.Fatalf("owner granting owner error: %v", err)
	}
	// a non-owner cannot grant the owner role.
	if _, err := h.svc.ChangeMemberRole(t.Context(), ChangeMemberRoleCommand{UserID: 1003, ConversationID: 1, TargetUserID: 1002, Role: "owner"}); !errors.Is(err, domain.ErrOnlyOwnerMayChangeOwner) {
		t.Errorf("non-owner granting owner err = %v, want ErrOnlyOwnerMayChangeOwner", err)
	}
	// owner cannot be demoted (M1).
	if _, err := h.svc.ChangeMemberRole(t.Context(), ChangeMemberRoleCommand{UserID: 1001, ConversationID: 1, TargetUserID: 1002, Role: "member"}); !errors.Is(err, domain.ErrCannotDemoteOwner) {
		t.Errorf("demoting owner err = %v, want ErrCannotDemoteOwner", err)
	}
}

func TestChangeRoleAdminManagesMembers(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001, [2]any{int64(1002), domain.RoleAdmin}, [2]any{int64(1003), domain.RoleMember}, [2]any{int64(1004), domain.RoleMember})

	if _, err := h.svc.ChangeMemberRole(t.Context(), ChangeMemberRoleCommand{UserID: 1002, ConversationID: 1, TargetUserID: 1003, Role: "admin"}); err != nil {
		t.Fatalf("admin promoting to admin error: %v", err)
	}
	if _, err := h.svc.ChangeMemberRole(t.Context(), ChangeMemberRoleCommand{UserID: 1003, ConversationID: 1, TargetUserID: 1002, Role: "member"}); !errors.Is(err, domain.ErrInsufficientRole) {
		t.Errorf("new admin demoting old admin err = %v, want ErrInsufficientRole", err)
	}
	// plain member cannot change any role.
	if _, err := h.svc.ChangeMemberRole(t.Context(), ChangeMemberRoleCommand{UserID: 1004, ConversationID: 1, TargetUserID: 1002, Role: "member"}); !errors.Is(err, domain.ErrInsufficientRole) {
		t.Errorf("member changing role err = %v, want ErrInsufficientRole", err)
	}
}

func TestChangeRoleInvalidRole(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001)
	_, err := h.svc.ChangeMemberRole(t.Context(), ChangeMemberRoleCommand{UserID: 1001, ConversationID: 1, TargetUserID: 1001, Role: "superuser"})
	ve, ok := err.(*domain.ValidationError)
	if !ok || ve.Field != "role" {
		t.Errorf("err = %v, want role validation", err)
	}
}
