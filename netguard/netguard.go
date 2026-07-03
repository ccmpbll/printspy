// Package netguard blocks outbound requests to loopback and link-local
// addresses (including the 169.254.169.254 cloud metadata endpoint), so a
// user-supplied printer URL can't turn PrintSpy's backend into an SSRF proxy.
// RFC1918 (LAN) addresses are allowed — that's where printers live.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"syscall"
)

func Transport() *http.Transport {
	dialer := &net.Dialer{
		Control: func(network, address string, c syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("netguard: invalid address %q", host)
			}
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				return fmt.Errorf("netguard: blocked request to %s", ip)
			}
			return nil
		},
	}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
	}
}
