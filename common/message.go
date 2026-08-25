package common

import (
	"fmt"
	"io"
	"strings"
)

// Messages are exchanged as fixed-size frames of MessageSize bytes:
//
//	bytes 0..9    identifier, null-padded
//	byte  10      reserved, always zero
//	bytes 11..255 parameter, null-padded
const (
	MessageSize         = 256
	MaxIdentifierLength = 10
	MaxParameterLength  = MessageSize - MaxIdentifierLength - 1
)

type Message struct {
	Identifier string
	Parameter  string
}

func ReadMessage(buf [MessageSize]byte) *Message {
	var message Message

	// Trim everything after \x00 (assuming both identifer and parameter are null-terminated strings)
	message.Identifier = strings.TrimRight(string(buf[:MaxIdentifierLength]), "\x00")
	message.Parameter = strings.TrimRight(string(buf[MaxIdentifierLength+1:]), "\x00")

	return &message
}

func WriteMessage(message *Message) ([]byte, error) {
	if len(message.Identifier) > MaxIdentifierLength {
		return nil, fmt.Errorf("message identifier %q exceeds %d bytes", message.Identifier, MaxIdentifierLength)
	}
	if len(message.Parameter) > MaxParameterLength {
		return nil, fmt.Errorf("message parameter exceeds %d bytes", MaxParameterLength)
	}

	var buf [MessageSize]byte

	copy(buf[:MaxIdentifierLength], message.Identifier)
	copy(buf[MaxIdentifierLength+1:], message.Parameter)

	return buf[:], nil
}

func ReadMessageFrom(r io.Reader) (*Message, error) {
	var buf [MessageSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return nil, err
	}
	return ReadMessage(buf), nil
}

func WriteMessageTo(w io.Writer, message *Message) error {
	payload, err := WriteMessage(message)
	if err != nil {
		return err
	}

	written, err := w.Write(payload)
	if err != nil {
		return err
	}
	if written != len(payload) {
		return io.ErrShortWrite
	}

	return nil
}
