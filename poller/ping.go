package poller

import (
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// pingHost sends a single ICMP echo request to host and waits for a reply.
// Used as a keepalive for printers (PrusaLink) whose wifi interface has been
// observed to drop off the network after a period of idle traffic.
func pingHost(host string, timeout time.Duration) error {
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return fmt.Errorf("open icmp socket: %w", err)
	}
	defer conn.Close()

	dst, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   os.Getpid() & 0xffff,
			Seq:  1,
			Data: []byte("printspy-keepalive"),
		},
	}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return err
	}
	if _, err := conn.WriteTo(wb, dst); err != nil {
		return fmt.Errorf("write icmp echo: %w", err)
	}

	deadline := time.Now().Add(timeout)
	conn.SetReadDeadline(deadline)
	rb := make([]byte, 512)

	// Raw ICMP sockets receive all ICMPv4 traffic on the host, not just
	// replies to this request - a concurrent ping to a different printer's
	// reply can arrive first on the same socket. Keep reading until our
	// own reply shows up or the deadline runs out, instead of bailing on
	// the first mismatched packet.
	for {
		n, from, err := conn.ReadFrom(rb)
		if err != nil {
			return fmt.Errorf("read icmp reply: %w", err)
		}
		ipAddr, ok := from.(*net.IPAddr)
		if !ok || !ipAddr.IP.Equal(dst.IP) {
			continue
		}

		reply, err := icmp.ParseMessage(1, rb[:n]) // 1 = ICMPv4 protocol number
		if err != nil {
			continue
		}
		if reply.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		return nil
	}
}
