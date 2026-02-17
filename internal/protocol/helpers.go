package protocol

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// NewMessage creates a new Message with an auto-generated ID and timestamp.
// The payload is marshaled to JSON from the provided value.
func NewMessage(from, to string, msgType MessageType, payload interface{}) (*Message, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling payload: %w", err)
	}

	return &Message{
		MessageID: uuid.New().String(),
		From:      from,
		To:        to,
		Type:      msgType,
		Payload:   raw,
		Timestamp: time.Now().UTC(),
	}, nil
}

// ParsePayload unmarshals the message payload into the target type T.
func ParsePayload[T any](msg *Message) (*T, error) {
	var result T
	if err := json.Unmarshal(msg.Payload, &result); err != nil {
		return nil, fmt.Errorf("unmarshaling payload as %T: %w", result, err)
	}
	return &result, nil
}
