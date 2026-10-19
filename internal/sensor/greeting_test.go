package sensor

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// startGreetingServer accepts one connection and writes the greeting, which is
// all the greeting probes ever ask a server to do.
func startGreetingServer(t *testing.T, greeting string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(greeting))
		// Keep the connection open long enough for the client to read the line;
		// closing immediately would race the read on some platforms.
		time.Sleep(200 * time.Millisecond)
	}()
	return ln.Addr().String()
}

func greetingContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSSHProberReadsTheBanner(t *testing.T) {
	// RFC 4253 allows lines before the identification line, and some appliances
	// use them; the prober has to skip those rather than judge the first line.
	addr := startGreetingServer(t, "hello from a slow appliance\r\nSSH-2.0-TestServer_1.0\r\n")
	got := SSHProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "ssh", Type: model.SensorSSH, Target: addr,
	}))

	if !got.Success {
		t.Fatalf("the banner was rejected: %s", got.Message)
	}
	if !strings.Contains(got.Message, "SSH-2.0-TestServer_1.0") {
		t.Errorf("message %q does not name the banner", got.Message)
	}
}

func TestSSHProberChecksTheExpectedBanner(t *testing.T) {
	addr := startGreetingServer(t, "SSH-2.0-OpenSSH_9.6p1\r\n")
	got := SSHProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "ssh", Type: model.SensorSSH, Target: addr,
		Options: model.Options{"expect": "Dropbear"},
	}))

	if got.Success {
		t.Fatalf("a banner without the expected text passed: %s", got.Message)
	}
	if !strings.Contains(got.Message, "Dropbear") {
		t.Errorf("message %q does not name what was missing", got.Message)
	}
}

func TestSSHProberRejectsANonSSHService(t *testing.T) {
	addr := startGreetingServer(t, "HTTP/1.1 400 Bad Request\r\n")
	got := SSHProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "ssh", Type: model.SensorSSH, Target: addr,
	}))

	if got.Success {
		t.Fatalf("a non-SSH greeting passed: %s", got.Message)
	}
}

func TestSMTPProberReadsTheGreeting(t *testing.T) {
	addr := startGreetingServer(t, "220 mail.test ESMTP Postfix\r\n")
	got := SMTPProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "smtp", Type: model.SensorSMTP, Target: addr,
	}))

	if !got.Success {
		t.Fatalf("the greeting was rejected: %s", got.Message)
	}
	if !strings.Contains(got.Message, "220 mail.test ESMTP Postfix") {
		t.Errorf("message %q does not carry the greeting", got.Message)
	}
}

func TestSMTPProberRejectsANon220Greeting(t *testing.T) {
	addr := startGreetingServer(t, "554 mail.test no service\r\n")
	got := SMTPProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "smtp", Type: model.SensorSMTP, Target: addr,
	}))

	if got.Success {
		t.Fatalf("a 554 greeting passed: %s", got.Message)
	}
	if !strings.Contains(got.Message, "220") {
		t.Errorf("message %q does not say what was expected", got.Message)
	}
}

func TestIMAPProberReadsTheGreeting(t *testing.T) {
	addr := startGreetingServer(t, "* OK [CAPABILITY IMAP4rev1] Dovecot ready.\r\n")
	got := IMAPProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "imap", Type: model.SensorIMAP, Target: addr,
	}))

	if !got.Success {
		t.Fatalf("the greeting was rejected: %s", got.Message)
	}
	if !strings.Contains(got.Message, "Dovecot ready") {
		t.Errorf("message %q does not carry the greeting", got.Message)
	}
}

func TestIMAPProberRejectsABYE(t *testing.T) {
	addr := startGreetingServer(t, "* BYE server shutting down\r\n")
	got := IMAPProber{}.Run(greetingContext(t), validSensor(model.Sensor{
		Name: "imap", Type: model.SensorIMAP, Target: addr,
	}))

	if got.Success {
		t.Fatalf("a BYE greeting passed: %s", got.Message)
	}
}
