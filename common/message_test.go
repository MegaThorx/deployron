package common

import (
	"strings"
	"testing"
)

func roundTrip(t *testing.T, message *Message) *Message {
	t.Helper()

	payload, err := WriteMessage(message)
	if err != nil {
		t.Fatalf("WriteMessage(%+v) failed: %v", message, err)
	}
	if len(payload) != MessageSize {
		t.Fatalf("payload is %d bytes, want %d", len(payload), MessageSize)
	}

	var buf [MessageSize]byte
	copy(buf[:], payload)
	return ReadMessage(buf)
}

func TestMessageRoundTrip(t *testing.T) {
	got := roundTrip(t, &Message{Identifier: "EXC_DEPLOY", Parameter: "mydeploy1"})

	if got.Identifier != "EXC_DEPLOY" {
		t.Errorf("Identifier = %q, want %q", got.Identifier, "EXC_DEPLOY")
	}
	if got.Parameter != "mydeploy1" {
		t.Errorf("Parameter = %q, want %q", got.Parameter, "mydeploy1")
	}
}

func TestMessageRoundTripMaxLengths(t *testing.T) {
	identifier := strings.Repeat("i", MaxIdentifierLength)
	parameter := strings.Repeat("p", MaxParameterLength)

	got := roundTrip(t, &Message{Identifier: identifier, Parameter: parameter})

	if got.Identifier != identifier {
		t.Errorf("Identifier = %q, want %q", got.Identifier, identifier)
	}
	if got.Parameter != parameter {
		t.Errorf("Parameter = %q, want %q", got.Parameter, parameter)
	}
}

func TestWriteMessageRejectsOverlongIdentifier(t *testing.T) {
	message := &Message{Identifier: strings.Repeat("i", MaxIdentifierLength+1)}
	if _, err := WriteMessage(message); err == nil {
		t.Error("WriteMessage accepted an overlong identifier")
	}
}

func TestWriteMessageRejectsOverlongParameter(t *testing.T) {
	message := &Message{Identifier: "EXC_DEPLOY", Parameter: strings.Repeat("p", MaxParameterLength+1)}
	if _, err := WriteMessage(message); err == nil {
		t.Error("WriteMessage accepted an overlong parameter")
	}
}
