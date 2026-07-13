package id

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// New returns a UUIDv7 string using the current UTC millisecond timestamp and
// cryptographically random payload bits.
func New() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("secure random source unavailable: " + err.Error())
	}
	milliseconds := uint64(time.Now().UTC().UnixMilli())
	value[0] = byte(milliseconds >> 40)
	value[1] = byte(milliseconds >> 32)
	value[2] = byte(milliseconds >> 24)
	value[3] = byte(milliseconds >> 16)
	value[4] = byte(milliseconds >> 8)
	value[5] = byte(milliseconds)
	value[6] = 0x70 | value[6]&0x0f
	value[8] = 0x80 | value[8]&0x3f

	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])
	return string(encoded)
}

func Timestamp(value string) (time.Time, bool) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return time.Time{}, false
	}
	raw := make([]byte, 16)
	compact := value[0:8] + value[9:13] + value[14:18] + value[19:23] + value[24:36]
	if _, err := hex.Decode(raw, []byte(compact)); err != nil || raw[6]>>4 != 7 || raw[8]>>6 != 2 {
		return time.Time{}, false
	}
	milliseconds := uint64(raw[0])<<40 |
		uint64(raw[1])<<32 |
		uint64(raw[2])<<24 |
		uint64(raw[3])<<16 |
		uint64(raw[4])<<8 |
		uint64(raw[5])
	return time.UnixMilli(int64(milliseconds)).UTC(), true
}
