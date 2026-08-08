package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	seq := int64(7)
	in := Frame{
		Version: ProtocolVersion,
		ID:      "op-1",
		Type:    EventMessageSend,
		Seq:     &seq,
		At:      time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC),
		Data:    json.RawMessage(`{"text":"hi"}`),
	}
	b, err := in.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := Decode(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Version != in.Version || out.ID != in.ID || out.Type != in.Type {
		t.Errorf("scalar mismatch: %+v", out)
	}
	if out.Seq == nil || *out.Seq != 7 {
		t.Errorf("seq = %v, want 7", out.Seq)
	}
	if !out.At.Equal(in.At) {
		t.Errorf("at = %v, want %v", out.At, in.At)
	}
	if string(out.Data) != `{"text":"hi"}` {
		t.Errorf("data = %s", out.Data)
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"not-json",
		`{"version":1}`,
		`{"version":99,"type":"ping"}`,
		`{"version":1,"type":""}`,
		`{"version":1,"type":"message.send","seq":"not-int"}`,
		`{"version":1,"type":"message.send","data":["not-object"]}`,
		`{"version":1,"type":"message.send","at":"not-a-time"}`,
	}
	for _, tc := range cases {
		if _, err := Decode([]byte(tc)); err == nil {
			t.Errorf("Decode(%q) = nil error, want error", tc)
		}
	}
}

func TestFrameVersionMismatch(t *testing.T) {
	raw := []byte(`{"version":2,"type":"ping"}`)
	if _, err := Decode(raw); err == nil {
		t.Error("Decode of unsupported protocol version must fail")
	}
}

func TestCloseCodeString(t *testing.T) {
	cases := map[CloseCode]string{
		CloseAuthInvalid:    "auth/token invalid",
		CloseSessionRevoked: "session revoked",
		CloseRateLimit:      "rate limit abuse",
		CloseProtocol:       "invalid frame / protocol violation",
		CloseSlowConsumer:   "slow consumer",
		CloseServerRestart:  "server restart",
		ClosePolicy:         "policy violation",
	}
	for code, want := range cases {
		if got := code.String(); got != want {
			t.Errorf("CloseCode(%d).String() = %q, want %q", code, got, want)
		}
	}
}
