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

	conn.SetReadDeadline(time.Now().Add(timeout))
	rb := make([]byte, 512)
	n, from, err := conn.ReadFrom(rb)
	if err != nil {
		return fmt.Errorf("read icmp reply: %w", err)
	}
	// Raw ICMP sockets receive all ICMPv4 traffic on the host, not just
	// replies to this request - a concurrent ping to a different printer
	// could otherwise be mistaken for this one's reply.
	if ipAddr, ok := from.(*net.IPAddr); !ok || !ipAddr.IP.Equal(dst.IP) {
		return fmt.Errorf("reply from unexpected host: %v", from)
	}

	reply, err := icmp.ParseMessage(1, rb[:n]) // 1 = ICMPv4 protocol number
	if err != nil {
		return err
	}
	if reply.Type != ipv4.ICMPTypeEchoReply {
		return fmt.Errorf("unexpected icmp reply type: %v", reply.Type)
	}
	return nil
}
