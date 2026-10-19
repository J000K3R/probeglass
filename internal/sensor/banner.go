package sensor

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// This file holds the small amount of plumbing the greeting sensors share:
// SSH, SMTP and IMAP all open a TCP connection, read one line the server sends
// unprompted, and judge it. Each one is still its own prober and its own file,
// because the line it expects and the reason it matters are different.

// hostPortDefault accepts "host", "host:port", a bare IPv4 or IPv6 address, and
// fills in defPort when the target carries no port. It returns the host on its
// own (for TLS verification) and the address to dial.
func hostPortDefault(raw, defPort string) (host, addr string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("target is required")
	}
	if strings.ContainsAny(raw, "/ \t") {
		return "", "", errors.New("target must be a host or host:port, without a path")
	}
	// A bare address, including an IPv6 one whose colons would otherwise look
	// like a port separator.
	if ip := net.ParseIP(strings.Trim(raw, "[]")); ip != nil {
		return ip.String(), net.JoinHostPort(ip.String(), defPort), nil
	}
	if h, port, splitErr := net.SplitHostPort(raw); splitErr == nil {
		if strings.TrimSpace(h) == "" {
			return "", "", errors.New("target must include a host")
		}
		n, convErr := strconv.Atoi(port)
		if convErr != nil || n < 1 || n > 65535 {
			return "", "", fmt.Errorf("port must be a number between 1 and 65535, got %q", port)
		}
		return h, raw, nil
	}
	if strings.Contains(raw, ":") {
		return "", "", fmt.Errorf("target must be host:port (for example mail.local:%s)", defPort)
	}
	return raw, net.JoinHostPort(raw, defPort), nil
}

// dialText opens the connection a greeting is read from, over TLS when asked.
// The returned note names the TLS version, or is empty for a plain connection.
//
// The read deadline comes from the context, which the scheduler and the Test
// button both bound by the sensor's timeout. The fallback is only for a caller
// that handed over a context with no deadline at all.
func dialText(ctx context.Context, addr string, useTLS bool, serverName string, insecure bool) (net.Conn, string, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, "", err
	}

	note := ""
	if useTLS {
		name := strings.TrimSpace(serverName)
		if name == "" {
			name, _, _ = net.SplitHostPort(addr)
		}
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         name,
			InsecureSkipVerify: insecure, //nolint:gosec // the user asked for it on this sensor
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, "", fmt.Errorf("TLS handshake failed: %w", err)
		}
		note = tls.VersionName(tlsConn.ConnectionState().Version)
		conn = tlsConn
	}

	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	return conn, note, nil
}

// readGreetingLine reads one CRLF-terminated line and strips the terminator. The
// buffer is small on purpose: a greeting is a line, and a service that streams
// megabytes at a monitor has already told it something is wrong.
func readGreetingLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
