//go:build integration

package ws

// End-to-end verification of the realtime gateway against the running
// container stack (DEVOPS.md §8 e2e). Requires APP_JWT_PRIVATE_KEY (the dev
// E2E signing seed the container verifies) and APP_PG_DSN pointing at the
// stack Postgres, plus the api-server on localhost:8080. Skips otherwise.
//
// It exercises the full production path: HTTP REST to set up a conversation,
// then the WebSocket gateway for presence fan-out (multi-device), typing
// indicators, and resume/replay after a gap.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	authsecurity "github.com/AkaneSakuramori/socialmedia/server/internal/auth/infra/security"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/idgen"
	rdomain "github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

const (
	e2eHTTPBase = "http://localhost:8080"
	e2eWSBase   = "ws://localhost:8080/v1/ws"
)

// e2eEnv returns the signing seed + PG DSN, skipping when not configured.
func e2eEnv(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	seedB64 := os.Getenv("APP_JWT_PRIVATE_KEY")
	dsn := os.Getenv("APP_PG_DSN")
	if seedB64 == "" || dsn == "" {
		t.Skip("e2e requires APP_JWT_PRIVATE_KEY and APP_PG_DSN against the running stack")
	}
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Skipf("APP_JWT_PRIVATE_KEY is not a valid %d-byte Ed25519 seed", ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), dsn
}

// e2ePrincipal is one minted identity with seeded DB rows.
type e2ePrincipal struct {
	userID    int64
	sessionID int64
	deviceID  string
	token     string
}

// e2eSeedPrincipal creates a user + active session row and mints its token.
func e2eSeedPrincipal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tf *authsecurity.TokenFactory, ids *idgen.Generator, tag string) e2ePrincipal {
	t.Helper()
	userID, err := ids.NextID()
	if err != nil {
		t.Fatalf("idgen: %v", err)
	}
	username := "e2e_" + tag + "_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, username, display_name, account_state, token_version) VALUES ($1,$2,$3,'active',0)`,
		userID, username, "E2E "+tag); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_sessions WHERE user_id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return e2eSeedDevice(t, ctx, pool, tf, ids, userID, tag)
}

// e2eSeedDevice seeds a second device (a new active session) for an existing
// user and mints its token — the multi-device test shape (same user, several
// sockets). The user row must already exist.
func e2eSeedDevice(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tf *authsecurity.TokenFactory, ids *idgen.Generator, userID int64, tag string) e2ePrincipal {
	t.Helper()
	sessionID, err := ids.NextID()
	if err != nil {
		t.Fatalf("idgen: %v", err)
	}
	deviceID := "e2e-dev-" + tag

	if _, err := pool.Exec(ctx,
		`INSERT INTO user_sessions (id, user_id, device_id, state) VALUES ($1,$2,$3,'active')`,
		sessionID, userID, deviceID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_sessions WHERE id = $1`, sessionID)
	})

	pair, err := tf.IssuePair(ctx, sessionID, userID, deviceID, 0, time.Now())
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return e2ePrincipal{userID: userID, sessionID: sessionID, deviceID: deviceID, token: pair.AccessToken}
}

// e2eHTTP performs one authenticated REST call with an idempotency key.
// deviceID must match the caller's seeded session (X-Device-Id, API.md §2.x).
func e2eHTTP(t *testing.T, method, path, token, deviceID, idemKey string, body any) map[string]any {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e2eHTTPBase+path, rdr)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device-Id", deviceID)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			out = map[string]any{"_raw": string(raw)}
		}
	}
	if resp.StatusCode >= 300 {
		t.Fatalf("http %s %s = %d: %s", method, path, resp.StatusCode, string(raw))
	}
	return out
}

// e2eConn is a live WebSocket connection plus a read helper.
type e2eConn struct {
	t    *testing.T
	conn *websocket.Conn
}

// dialE2E connects, completes the hello handshake, and returns the socket.
func dialE2E(t *testing.T, p e2ePrincipal) (*e2eConn, *rdomain.Frame) {
	t.Helper()
	url := e2eWSBase + "?access_token=" + p.token + "&device_id=" + p.deviceID
	c, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{Subprotocols: []string{rdomain.Subprotocol}})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	ec := &e2eConn{t: t, conn: c}
	// The first frame is always hello (§17.1); the query token authenticates
	// at upgrade, and hello carries the bootstrap cursors.
	ec.write(rdomain.EventHello, map[string]any{
		"session_id":      p.sessionID,
		"client_version":  "e2e",
		"last_seq":        0,
		"last_global_seq": 0,
	})
	ack, err := ec.read(time.Minute)
	if err != nil {
		t.Fatalf("hello_ack: %v", err)
	}
	return ec, ack
}

func (c *e2eConn) write(typ string, payload any) {
	c.t.Helper()
	f := rdomain.Frame{Version: rdomain.ProtocolVersion, ID: "c-" + strconv.FormatInt(time.Now().UnixNano(), 10), Type: typ}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			c.t.Fatalf("marshal %s: %v", typ, err)
		}
		f.Data = b
	}
	raw, err := f.Encode()
	if err != nil {
		c.t.Fatalf("encode %s: %v", typ, err)
	}
	c.t.Logf("e2e C2S %s", typ)
	if err := c.conn.Write(context.Background(), websocket.MessageText, raw); err != nil {
		c.t.Fatalf("write %s: %v", typ, err)
	}
}

func (c *e2eConn) read(timeout time.Duration) (*rdomain.Frame, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, b, err := c.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	return rdomain.Decode(b)
}

func (c *e2eConn) close() {
	_ = c.conn.CloseNow()
}

// e2eReadType waits until a frame of the given type arrives (skipping acks).
func e2eReadType(t *testing.T, c *e2eConn, typ string, timeout time.Duration) *rdomain.Frame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f, err := c.read(time.Until(deadline))
		if err != nil {
			t.Fatalf("read while waiting for %s: %v", typ, err)
		}
		if f.Type == typ {
			return f
		}
		if f.Type == rdomain.EventError || f.Type == rdomain.EventResumeRejected {
			t.Logf("e2e saw %s frame while waiting for %s: %s", f.Type, typ, f.Data)
		}
	}
	t.Fatalf("timed out waiting for %s", typ)
	return nil
}

func TestE2EHandshakeAndHelloAck(t *testing.T) {
	key, dsn := e2eEnv(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	ids, _ := idgen.New(997, idgen.DefaultEpoch)
	tf := e2eTokenFactory(t, key)

	p := e2eSeedPrincipal(t, ctx, pool, tf, ids, "hs-a")
	conn, ack := dialE2E(t, p)
	defer conn.close()

	var body struct {
		ConnectionID string `json:"connection_id"`
		SessionID    int64  `json:"session_id"`
		GlobalSeq    int64  `json:"global_seq"`
	}
	if err := json.Unmarshal(ack.Data, &body); err != nil {
		t.Fatalf("hello_ack data: %v", err)
	}
	if ack.Type != rdomain.EventHelloAck {
		t.Fatalf("first frame = %q, want hello_ack", ack.Type)
	}
	if body.SessionID != p.sessionID {
		t.Errorf("session_id = %d, want %d", body.SessionID, p.sessionID)
	}
	if body.ConnectionID == "" {
		t.Error("hello_ack missing connection_id")
	}
}

func e2eTokenFactory(t *testing.T, key ed25519.PrivateKey) *authsecurity.TokenFactory {
	t.Helper()
	tf, err := authsecurity.NewTokenFactory(authsecurity.TokenConfig{
		SigningKey: key,
		Issuer:     "https://api.socialmedia.example",
		Audience:   "inchat-api",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("token factory: %v", err)
	}
	return tf
}

// TestE2EPresenceAndTyping drives the ephemeral backplane end-to-end: a
// presence.update and typing indicators from one device must reach the other
// participant's socket through Redis pub/sub + the dispatcher.
func TestE2EPresenceAndTyping(t *testing.T) {
	key, dsn := e2eEnv(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	ids, _ := idgen.New(996, idgen.DefaultEpoch)
	tf := e2eTokenFactory(t, key)

	a := e2eSeedPrincipal(t, ctx, pool, tf, ids, "pa-a")
	b := e2eSeedPrincipal(t, ctx, pool, tf, ids, "pa-b")
	a2 := e2eSeedDevice(t, ctx, pool, tf, ids, a.userID, "pa-a2") // A's second device

	// Direct conversation A ↔ B over REST.
	res := e2eHTTP(t, "POST", "/v1/conversations", a.token, a.deviceID, "idem-conv-"+strconv.FormatInt(time.Now().UnixNano(), 10),
		map[string]any{"type": "direct", "participant_ids": []string{strconv.FormatInt(b.userID, 10)}})
	convID, ok := res["id"].(string)
	if !ok {
		t.Fatalf("create conversation: no id in %v", res)
	}

	// A (two devices) + B subscribe; B observes A's presence and typing.
	ca, _ := dialE2E(t, a)
	defer ca.close()
	ca2, _ := dialE2E(t, a2)
	defer ca2.close()
	cb, _ := dialE2E(t, b)
	defer cb.close()

	ca.write(rdomain.EventSubscribe, map[string]any{"conversation_ids": []int64{mustConv(t, convID)}})
	if f := e2eReadType(t, ca, rdomain.EventServerAck, 5*time.Second); string(f.Data) == "" {
		t.Fatal("A subscribe returned no ack")
	}
	cb.write(rdomain.EventSubscribe, map[string]any{"conversation_ids": []int64{mustConv(t, convID)}})
	if f := e2eReadType(t, cb, rdomain.EventServerAck, 5*time.Second); string(f.Data) == "" {
		t.Fatal("B subscribe returned no ack")
	}
	ca2.write(rdomain.EventSubscribe, map[string]any{"conversation_ids": []int64{mustConv(t, convID)}})
	if f := e2eReadType(t, ca2, rdomain.EventServerAck, 5*time.Second); string(f.Data) == "" {
		t.Fatal("A device 2 subscribe returned no ack")
	}

	// A transitions to busy — every subscriber (B and A's second device) sees
	// presence.changed with the same status.
	ca.write(rdomain.EventPresenceUpdate, map[string]any{"status": "busy", "custom_status": "e2e focus"})

	for _, c := range []*e2eConn{cb, ca2} {
		f := e2eReadType(t, c, rdomain.EventPresenceChanged, 10*time.Second)
		var pbody struct {
			UserID   string `json:"user_id"`
			Presence struct {
				Status string `json:"status"`
			} `json:"presence"`
		}
		if err := json.Unmarshal(f.Data, &pbody); err != nil {
			t.Fatalf("presence.changed data: %v", err)
		}
		if pbody.UserID != strconv.FormatInt(a.userID, 10) {
			t.Errorf("presence user_id = %q, want %d", pbody.UserID, a.userID)
		}
		if pbody.Presence.Status != "busy" {
			t.Errorf("presence status = %q, want busy", pbody.Presence.Status)
		}
	}

	// A types in the conversation — B (and A's second device) get the
	// indicator. typing.stop is never throttled and always fans out.
	ca.write(rdomain.EventTypingStart, map[string]any{"conversation_id": mustConv(t, convID)})
	f := e2eReadType(t, cb, rdomain.EventTypingIndicator, 10*time.Second)
	var tbody struct {
		ConversationID string `json:"conversation_id"`
		UserID         string `json:"user_id"`
		Status         string `json:"status"`
	}
	if err := json.Unmarshal(f.Data, &tbody); err != nil {
		t.Fatalf("typing.indicator data: %v", err)
	}
	if tbody.UserID != strconv.FormatInt(a.userID, 10) || tbody.Status != "typing" {
		t.Errorf("typing = %+v, want user %d typing", tbody, a.userID)
	}

	ca.write(rdomain.EventTypingStop, map[string]any{"conversation_id": mustConv(t, convID)})
	f = e2eReadType(t, cb, rdomain.EventTypingIndicator, 10*time.Second)
	if err := json.Unmarshal(f.Data, &tbody); err != nil {
		t.Fatalf("typing.indicator stop data: %v", err)
	}
	if tbody.Status != "stopped" {
		t.Errorf("typing stop status = %q, want stopped", tbody.Status)
	}
}

func mustConv(t *testing.T, s string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("conv id %q: %v", s, err)
	}
	return v
}

// TestE2EResumeReplaysGap exercises the full resume protocol over the running
// stack: a message committed while a client is offline is replayed on resume
// via the shared replay buffer (relay → dispatcher → buffer → handler).
func TestE2EResumeReplaysGap(t *testing.T) {
	key, dsn := e2eEnv(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	ids, _ := idgen.New(995, idgen.DefaultEpoch)
	tf := e2eTokenFactory(t, key)

	a := e2eSeedPrincipal(t, ctx, pool, tf, ids, "rs-a")
	b := e2eSeedPrincipal(t, ctx, pool, tf, ids, "rs-b")

	res := e2eHTTP(t, "POST", "/v1/conversations", a.token, a.deviceID, "idem-conv-"+strconv.FormatInt(time.Now().UnixNano(), 10),
		map[string]any{"type": "direct", "participant_ids": []string{strconv.FormatInt(b.userID, 10)}})
	convID := mustConv(t, res["id"].(string))

	// B stays online and subscribed; A connects, subscribes, notes the head,
	// then drops.
	cb, _ := dialE2E(t, b)
	defer cb.close()
	cb.write(rdomain.EventSubscribe, map[string]any{"conversation_ids": []int64{convID}})

	ca, ack := dialE2E(t, a)
	ca.write(rdomain.EventSubscribe, map[string]any{"conversation_ids": []int64{convID}})
	var hello struct {
		GlobalSeq int64 `json:"global_seq"`
	}
	if err := json.Unmarshal(ack.Data, &hello); err != nil {
		t.Fatalf("hello_ack data: %v", err)
	}
	ca.close() // A goes offline

	// While A is offline, B sends a message: change_log row → relay → backplane
	// → dispatcher appends to the replay buffer and fans out to B live.
	e2eHTTP(t, "POST", fmt.Sprintf("/v1/conversations/%d/messages", convID), b.token, b.deviceID,
		"idem-msg-"+strconv.FormatInt(time.Now().UnixNano(), 10),
		map[string]any{"type": "text", "client_msg_id": "e2e-msg-1", "content": map[string]any{"text": "while you were away"}})

	// B sees the message live.
	f := e2eReadType(t, cb, rdomain.EventMessageCreated, 15*time.Second)
	var mbody map[string]any
	if err := json.Unmarshal(f.Data, &mbody); err != nil {
		t.Fatalf("message.created data: %v", err)
	}
	gotConv := "?"
	if cf, ok := mbody["conversation_id"].(float64); ok {
		gotConv = strconv.FormatInt(int64(cf), 10)
	}
	if gotConv != strconv.FormatInt(convID, 10) {
		t.Errorf("message conversation_id = %q, want %d", gotConv, convID)
	}

	// A resumes from the head it saw before going offline: the missed message
	// must be replayed (resume_ack carries it) with a fresh contiguous seq.
	ca2, _ := dialE2E(t, a)
	defer ca2.close()
	ca2.write(rdomain.EventResume, map[string]any{
		"last_seq":        0,
		"last_global_seq": hello.GlobalSeq,
		"session_id":      a.sessionID,
	})

	rf := e2eReadType(t, ca2, rdomain.EventResumeAck, 10*time.Second)
	var rbody struct {
		FromSeq   int64 `json:"from_seq"`
		GlobalSeq int64 `json:"global_seq"`
		Replay    []struct {
			Type string `json:"type"`
			Seq  int64  `json:"seq"`
		} `json:"replay"`
	}
	if err := json.Unmarshal(rf.Data, &rbody); err != nil {
		t.Fatalf("resume_ack data: %v", err)
	}
	if len(rbody.Replay) == 0 {
		t.Fatalf("resume_ack carried no replay; gap %d→? was not buffered", hello.GlobalSeq)
	}
	replayed := false
	for _, ev := range rbody.Replay {
		if ev.Type == rdomain.EventMessageCreated {
			replayed = true
			break
		}
	}
	if !replayed {
		t.Errorf("resume replay %+v missing message.created", rbody.Replay)
	}
	if rbody.GlobalSeq < hello.GlobalSeq {
		t.Errorf("resume_ack global_seq = %d < pre-offline head %d", rbody.GlobalSeq, hello.GlobalSeq)
	}
}
