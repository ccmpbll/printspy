package octoprint

import (
	"bytes"
	"testing"
)

func TestReadBodyCapsOversizeResponses(t *testing.T) {
	if _, err := readBody(bytes.NewReader(make([]byte, maxBody+1))); err == nil {
		t.Error("oversize body accepted")
	}
	if b, err := readBody(bytes.NewReader(make([]byte, 1024))); err != nil || len(b) != 1024 {
		t.Errorf("normal body: len=%d err=%v", len(b), err)
	}
}
