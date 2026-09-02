package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCodecRoundTripAndOversize(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	if err := enc.Encode(Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "daemon.hello"}); err != nil {
		t.Fatal(err)
	}
	dec := NewDecoder(&buf, 4<<20)
	msg, err := dec.Next()
	if err != nil || msg.Method != "daemon.hello" {
		t.Fatalf("got %+v, %v", msg, err)
	}
	big := strings.Repeat("x", 5<<20)
	dec = NewDecoder(strings.NewReader(`{"jsonrpc":"2.0","method":"`+big+`"}`+"\n"), 4<<20)
	if _, err := dec.Next(); !errors.Is(err, ErrOversize) {
		t.Fatalf("want ErrOversize, got %v", err)
	}
}

func TestDecoderClassifiesNotifications(t *testing.T) {
	in := `{"jsonrpc":"2.0","method":"stream.chunk","params":{"id":"s1"}}` + "\n" +
		`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}` + "\n"
	dec := NewDecoder(strings.NewReader(in), 1<<20)
	n, err := dec.Next()
	if err != nil || !n.IsNotification() {
		t.Fatalf("first message: %+v %v", n, err)
	}
	r, err := dec.Next()
	if err != nil || r.IsNotification() {
		t.Fatalf("second message: %+v %v", r, err)
	}
	if string(r.ID) != "7" {
		t.Fatalf("id = %s", r.ID)
	}
	if !r.IsResponse() {
		t.Fatalf("second message should be a response: %+v", r)
	}
}

func TestDecoderEOF(t *testing.T) {
	dec := NewDecoder(strings.NewReader(""), 1<<20)
	if _, err := dec.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

func TestDecoderSkipsBlankLinesAndRecoversAfterOversize(t *testing.T) {
	big := strings.Repeat("x", 300)
	in := "\n" + `{"jsonrpc":"2.0","method":"` + big + `"}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"daemon.status"}` + "\n"
	dec := NewDecoder(strings.NewReader(in), 128)
	if _, err := dec.Next(); !errors.Is(err, ErrOversize) {
		t.Fatalf("want ErrOversize, got %v", err)
	}
	m, err := dec.Next()
	if err != nil || m.Method != "daemon.status" {
		t.Fatalf("decoder did not resync: %+v %v", m, err)
	}
}

func TestDecoderMalformedJSONIsInvalidParams(t *testing.T) {
	dec := NewDecoder(strings.NewReader("{not json}\n"), 1<<20)
	_, err := dec.Next()
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeInvalidParams {
		t.Fatalf("want invalid_params Error, got %v", err)
	}
}
