package netguard

import (
	"context"
	"strings"
	"testing"
)

func TestTransportBlocksUnsafeAddresses(t *testing.T) {
	tr := Transport()
	for _, addr := range []string{"127.0.0.1:1", "0.0.0.0:1", "[::]:1", "169.254.169.254:80"} {
		_, err := tr.DialContext(context.Background(), "tcp", addr)
		if err == nil || !strings.Contains(err.Error(), "netguard") {
			t.Errorf("%s: want netguard block, got %v", addr, err)
		}
	}
}
