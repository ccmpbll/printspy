package printmeta

import (
	"bytes"
	"compress/zlib"
	"testing"
)

func deflate(t *testing.T, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	w.Write(make([]byte, n))
	w.Close()
	return buf.Bytes()
}

func TestDecodeBytesRejectsZlibBomb(t *testing.T) {
	bomb := deflate(t, 64<<20)
	// Header lies: claims 10 bytes.
	if _, err := decodeBytes(bomb, 1, 10); err == nil {
		t.Error("stream larger than header size was accepted")
	}
	// Header honestly claims more than the cap.
	if _, err := decodeBytes(bomb, 1, 64<<20); err == nil {
		t.Error("size above maxDecodedBlock was accepted")
	}
}

func TestDecodeBytesAcceptsMatchingSize(t *testing.T) {
	out, err := decodeBytes(deflate(t, 1000), 1, 1000)
	if err != nil || len(out) != 1000 {
		t.Fatalf("len=%d err=%v", len(out), err)
	}
}
