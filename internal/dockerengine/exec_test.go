package dockerengine

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func stream(stream byte, data string) []byte {
	buf := make([]byte, 8+len(data))
	buf[0] = stream
	binary.BigEndian.PutUint32(buf[4:8], uint32(len(data)))
	copy(buf[8:], data)
	return buf
}
func TestReadMultiplexed(t *testing.T) {
	raw := append(stream(1, "{\"peers\":[]}"), stream(2, "warning")...)
	out, stderr, err := readMultiplexed(raw)
	if err != nil || string(out) != "{\"peers\":[]}" || stderr != "warning" {
		t.Fatalf("unexpected result: %q %q %v", out, stderr, err)
	}
	if _, _, err := readMultiplexed([]byte{1, 0}); err == nil {
		t.Fatal("truncated stream accepted")
	}
	bad := bytes.Clone(stream(1, "ok"))
	bad[0] = 3
	if _, _, err := readMultiplexed(bad); err == nil {
		t.Fatal("unknown stream accepted")
	}
}
