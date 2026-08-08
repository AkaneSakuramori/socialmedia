package application

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// messagePageEnrichment carries the per-page lookups that turn domain.Message
// rows into the §8.1 wire shape in a bounded number of queries.
type messagePageEnrichment struct {
	senders        map[int64]string
	replies        map[int64]domain.ReplyTo // message_id -> rendered reply
	reactionCounts map[int64]map[string]int64
	reactionUsers  map[int64]map[string][]int64
	cursors        map[int64]domain.CursorRow // user_id -> cursors
	readAt         map[int64]*time.Time       // user_id -> last_read_at
}

// enrichPage loads every derived field a page of messages needs: sender
// display names, reply targets, reaction aggregates, and member cursors for
// status/read_by derivation.
func (s *service) enrichPage(ctx context.Context, msgs []domain.Message) (*messagePageEnrichment, error) {
	e := &messagePageEnrichment{
		senders:        map[int64]string{},
		replies:        map[int64]domain.ReplyTo{},
		reactionCounts: map[int64]map[string]int64{},
		reactionUsers:  map[int64]map[string][]int64{},
		cursors:        map[int64]domain.CursorRow{},
		readAt:         map[int64]*time.Time{},
	}

	ids := make([]int64, 0, len(msgs))
	replyIDs := make([]int64, 0, len(msgs))
	senderIDs := make([]int64, 0, len(msgs))
	seenSender := map[int64]bool{}
	for i := range msgs {
		m := &msgs[i]
		ids = append(ids, m.ID)
		if m.ReplyToID != nil {
			replyIDs = append(replyIDs, *m.ReplyToID)
		}
		if sender := m.SenderIDOrZero(); sender != 0 && !seenSender[sender] {
			seenSender[sender] = true
			senderIDs = append(senderIDs, sender)
		}
	}

	if len(senderIDs) > 0 {
		users, err := s.deps.Users.ListByIDs(ctx, s.deps.DB, senderIDs)
		if err != nil {
			return nil, err
		}
		e.senders = displayNames(users)
	}

	if len(msgs) > 0 {
		counts, err := s.deps.Reactions.CountsByMessages(ctx, ids)
		if err != nil {
			return nil, err
		}
		usersByEmoji, err := s.deps.Reactions.UserIDsByMessages(ctx, ids)
		if err != nil {
			return nil, err
		}
		e.reactionCounts = counts
		e.reactionUsers = usersByEmoji

		// Cursors for status/read_by (one query per page).
		cursors, err := s.deps.Memberships.CursorsByConversation(ctx, msgs[0].ConversationID)
		if err != nil {
			return nil, err
		}
		for _, c := range cursors {
			e.cursors[c.UserID] = c
		}
		receipts, err := s.deps.Memberships.ListReceipts(ctx, msgs[0].ConversationID)
		if err != nil {
			return nil, err
		}
		for _, rc := range receipts {
			e.readAt[rc.UserID] = rc.LastReadAt
		}
	}

	for _, id := range replyIDs {
		reply, err := s.deps.Messages.FindByID(ctx, id)
		if err != nil {
			if errors.Is(err, domain.ErrMessageNotFound) {
				continue // purged/tombstoned reply → omit
			}
			return nil, err
		}
		rt := domain.ReplyTo{
			ID:   strconv.FormatInt(reply.ID, 10),
			Type: reply.RenderedType(),
		}
		if reply.SenderID != nil {
			sid := strconv.FormatInt(*reply.SenderID, 10)
			rt.SenderID = &sid
		}
		if !reply.Deleted() && reply.Content != nil {
			text := *reply.Content
			rt.Text = &text
		}
		e.replies[id] = rt
	}

	return e, nil
}

// messageView renders one §8.1 message from a domain.Message + enrichment.
func (s *service) messageView(m domain.Message, e *messagePageEnrichment) MessageView {
	var senderIDStr *string
	if m.SenderID != nil {
		sid := strconv.FormatInt(*m.SenderID, 10)
		senderIDStr = &sid
	}

	v := MessageView{
		ID:             strconv.FormatInt(m.ID, 10),
		ConversationID: strconv.FormatInt(m.ConversationID, 10),
		Sequence:       strconv.FormatInt(m.Sequence, 10),
		SenderID:       senderIDStr,
		Type:           string(m.RenderedType()),
		Media:          []domain.Attachment{},
		ClientMsgID:    m.ClientMsgID,
		CreatedAt:      m.CreatedAt,
		EditedAt:       m.EditedAt,
		Mentions:       []string{},
		Reactions:      []domain.Reaction{},
		ReadBy:         []ReadByView{},
		GlobalSeq:      strconv.FormatInt(m.GlobalSeq, 10),
	}

	if m.SenderID != nil {
		v.Sender = &SenderView{DisplayName: e.senders[*m.SenderID]}
	}

	// Deleted messages render with a stripped body and no content.
	if !m.Deleted() {
		if m.Content != nil {
			text := *m.Content
			v.Content = &MessageText{Text: &text}
		}
		v.Media = s.decodeEnvelope(m.AttachmentEnvelope)
	}

	for _, mid := range m.Mentions {
		v.Mentions = append(v.Mentions, strconv.FormatInt(mid, 10))
	}

	if m.ReplyToID != nil {
		if rt, ok := e.replies[*m.ReplyToID]; ok {
			v.ReplyTo = &ReplyToView{
				ID:       rt.ID,
				SenderID: rt.SenderID,
				Content:  MessageText{Text: rt.Text},
			}
		}
	}

	// Reactions: aggregate per emoji, newest reactors first.
	emojiCounts := e.reactionCounts[m.ID]
	emojiUsers := e.reactionUsers[m.ID]
	emojis := make([]string, 0, len(emojiCounts))
	for emoji := range emojiCounts {
		emojis = append(emojis, emoji)
	}
	sort.Strings(emojis)
	for _, emoji := range emojis {
		v.Reactions = append(v.Reactions, domain.Reaction{
			Emoji:   emoji,
			Count:   emojiCounts[emoji],
			UserIDs: emojiUsers[emoji],
		})
	}

	// Status + read_by from the member cursors (sender view): read when any
	// other member has read past the sequence, delivered when any has
	// delivered, else sent.
	if !m.Deleted() {
		delivered, read := false, false
		sender := m.SenderIDOrZero()
		for uid, c := range e.cursors {
			if uid == sender {
				continue
			}
			if c.LastReadSeq >= m.Sequence {
				read = true
				v.ReadBy = append(v.ReadBy, ReadByView{
					UserID: strconv.FormatInt(uid, 10),
					At:     e.readAt[uid],
				})
			}
			if c.LastDeliveredSeq >= m.Sequence {
				delivered = true
			}
		}
		switch {
		case read:
			v.Status = "read"
		case delivered:
			v.Status = "delivered"
		default:
			v.Status = "sent"
		}
	} else {
		v.Status = "sent"
	}

	return v
}

// decodeEnvelope parses the stored attachment_envelope jsonb (a list of media
// refs + captions). Malformed data degrades to an empty list.
func (s *service) decodeEnvelope(raw []byte) []domain.Attachment {
	return decodeEnvelopePlain(raw)
}

// decodeEnvelopePlain is the package-level envelope decoder (also used by the
// outbox payload builder).
func decodeEnvelopePlain(raw []byte) []domain.Attachment {
	if len(raw) == 0 {
		return []domain.Attachment{}
	}
	var out []domain.Attachment
	if err := json.Unmarshal(raw, &out); err != nil {
		return []domain.Attachment{}
	}
	return out
}

// encodeEnvelope marshals the media envelope to jsonb (nil media → nil).
func encodeEnvelope(media []domain.Attachment) []byte {
	if len(media) == 0 {
		return nil
	}
	b, err := json.Marshal(media)
	if err != nil {
		return nil
	}
	return b
}

// snippet derives the chat-list preview text for a message (DATABASE.md §5.1
// last_message_snippet). Text is truncated; media-only messages show a
// placeholder.
func (s *service) snippet(m domain.Message) *string {
	if m.Content != nil {
		return truncate(*m.Content, 120)
	}
	if m.Type == domain.MessageTypeMedia {
		s := "📎 Media"
		return &s
	}
	return nil
}

// truncate returns a rune-safe prefix of s up to max runes.
func truncate(s string, max int) *string {
	r := []rune(s)
	if len(r) > max {
		s = string(r[:max]) + "…"
	}
	return &s
}
