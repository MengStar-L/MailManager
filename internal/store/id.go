package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// NewID returns a UUIDv7 string. Its first 48 bits are Unix milliseconds,
// making IDs time-sortable without sacrificing cryptographic randomness.
func NewID() (string, error) {
	var id [16]byte
	millis := uint64(time.Now().UTC().UnixMilli())
	id[0] = byte(millis >> 40)
	id[1] = byte(millis >> 32)
	id[2] = byte(millis >> 24)
	id[3] = byte(millis >> 16)
	id[4] = byte(millis >> 8)
	id[5] = byte(millis)
	if _, err := rand.Read(id[6:]); err != nil {
		return "", fmt.Errorf("generate UUID randomness: %w", err)
	}
	id[6] = (id[6] & 0x0f) | 0x70
	id[8] = (id[8] & 0x3f) | 0x80

	var encoded [36]byte
	hex.Encode(encoded[0:8], id[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], id[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], id[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], id[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], id[10:16])
	return string(encoded[:]), nil
}
