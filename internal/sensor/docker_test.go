package sensor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// ---------------------------------------------------------------------------
// the stub
//
// A real unix socket with a real http.Server behind it, the same shape as the
// TCP and DNS tests: a stub that speaks the protocol is what catches a wrong
// path or a header that was never sent, and unlike a mock it cannot agree with
// the code by accident.
// ---------------------------------------------------------------------------

// requestLog records what the probe actually asked for, so a test can assert on
// the method and the path rather than only on the outcome.
type requestLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *requestLog) note(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// The escaped path, not the decoded one: that is what travelled, and it is
	// what shows whether the container name was escaped on the way out.
	l.lines = append(l.lines, r.Method+" "+r.URL.EscapedPath())
}

func (l *requestLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// answer is a handler that records the request and replies with a fixed status
// and body.
func (l *requestLog) answer(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l.note(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// shortTempDir is a temporary directory with a path short enough to hold a unix
// socket. t.TempDir() is not: it puts the test's own name in the path, and on
// macOS a socket path is capped at around a hundred bytes, past which the dial
// fails with something that has nothing to do with what the test is about.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wgd")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// absentSocket is a socket path that nothing is listening on and no file exists
// for — the state a mount that was never added leaves behind.
func absentSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortTempDir(t), "absent.sock")
}

// dockerStub serves h on a unix socket and returns its path.
func dockerStub(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	socket := filepath.Join(shortTempDir(t), "d.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", socket, err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return socket
}

// inspectBody builds the part of a container's inspect answer this sensor reads.
// An empty health means the daemon sent no Health object at all, which is what a
// container without a HEALTHCHECK looks like.
func inspectBody(name, status string, running bool, health string, restarts int) string {
	healthJSON := "null"
	if health != "" {
		healthJSON = fmt.Sprintf(`{"Status":%q,"FailingStreak":0}`, health)
	}
	return fmt.Sprintf(
		`{"Id":"abc123","Name":%q,"State":{"Status":%q,"Running":%t,"Health":%s},"RestartCount":%d}`,
		name, status, running, healthJSON, restarts)
}

// dockerSensor builds a sensor on socket. An explicit socket in opts wins, so a
// test can say "the default" or "not a path at all" as well as a stub.
func dockerSensor(socket string, opts model.Options) model.Sensor {
	merged := model.Options{}
	for k, v := range opts {
		merged[k] = v
	}
	if _, ok := merged["socket"]; !ok {
		merged["socket"] = socket
	}
	return model.Sensor{
		Name: "container", Type: model.SensorDocker, Target: "nginx",
		Interval: 60, Timeout: 10, Retries: 1, Enabled: true, Options: merged,
	}
}

func runDocker(t *testing.T, c model.Sensor) Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return DockerProber{}.Run(ctx, c)
}

// ---------------------------------------------------------------------------
// the happy paths
// ---------------------------------------------------------------------------

func TestDockerRunOnAHealthyContainer(t *testing.T) {
	log := &requestLog{}
	socket := dockerStub(t, log.answer(http.StatusOK,
		inspectBody("/nginx", "running", true, "healthy", 0)))

	out := runDocker(t, dockerSensor(socket, nil))
	if !out.Success {
		t.Fatalf("a healthy container was reported down: %s", out.Message)
	}
	for _, want := range []string{"nginx", "running", "healthy", "0 restart(s)"} {
		if !strings.Contains(out.Message, want) {
			t.Errorf("message %q does not mention %q", out.Message, want)
		}
	}
	// The restart count is information, not a protocol status: every other
	// prober leaves StatusCode for the protocol, and this one must too.
	if out.StatusCode != 0 {
		t.Errorf("StatusCode = %d, want 0", out.StatusCode)
	}
	// A container name is escaped into the path, and the whole API is one GET.
	if got := log.all(); len(got) != 1 || got[0] != "GET /containers/nginx/json" {
		t.Errorf("the daemon was asked %v, want one GET /containers/nginx/json", got)
	}
}

func TestDockerRunEscapesTheContainerName(t *testing.T) {
	log := &requestLog{}
	socket := dockerStub(t, log.answer(http.StatusOK,
		inspectBody("/my container", "running", true, "", 0)))

	c := dockerSensor(socket, model.Options{"expect_health": ""})
	c.Target = "my container"
	out := runDocker(t, c)
	if !out.Success {
		t.Fatalf("unexpected failure: %s", out.Message)
	}
	if got := log.all(); len(got) != 1 || got[0] != "GET /containers/my%20container/json" {
		t.Errorf("the daemon was asked %v, want the name escaped in the path", got)
	}
	// The name in the message is the daemon's, without the leading slash it puts
	// in front of every container name.
	if !strings.HasPrefix(out.Message, "my container · ") {
		t.Errorf("message %q should start with the container name", out.Message)
	}
}

func TestDockerRunWithoutAHealthCheck(t *testing.T) {
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusOK,
		inspectBody("/nginx", "running", true, "", 2)))

	// expect_health defaults to healthy, and a container with no HEALTHCHECK
	// cannot satisfy it. Saying so is the honest answer: the sensor was asked to
	// watch something that is not being measured.
	out := runDocker(t, dockerSensor(socket, nil))
	if out.Success {
		t.Fatalf("a container with no health check passed a health assertion: %s", out.Message)
	}
	if !strings.Contains(out.Message, "no health check") {
		t.Errorf("message %q should say there is no health check", out.Message)
	}
	// And it has to name the way out. No status can satisfy the sensor, so a
	// message that only reports the mismatch leaves the operator to find the
	// option that switches it off.
	if !strings.Contains(out.Message, "expect_health") {
		t.Errorf("message %q should name the option that switches the check off", out.Message)
	}
}

func TestDockerRunWithTheHealthCheckSwitchedOff(t *testing.T) {
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusOK,
		inspectBody("/nginx", "running", true, "", 4)))

	out := runDocker(t, dockerSensor(socket, model.Options{"expect_health": ""}))
	if !out.Success {
		t.Fatalf("health checking off should have passed: %s", out.Message)
	}
	if strings.Contains(out.Message, "healthy") {
		t.Errorf("message %q mentions health although the check is off", out.Message)
	}
	// The restart count is still reported: it is what tells an operator that a
	// limit is worth setting.
	if !strings.Contains(out.Message, "4 restart(s)") {
		t.Errorf("message %q lost the restart count", out.Message)
	}
}

func TestDockerRunShowsTheHealthItDidNotAssertOn(t *testing.T) {
	// With the check off the health status is not a verdict — but the daemon
	// reported it, and hiding an unhealthy container because nobody asked about
	// its health would throw away the one hint that explains the restarts.
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusOK,
		inspectBody("/nginx", "running", true, "unhealthy", 7)))

	out := runDocker(t, dockerSensor(socket, model.Options{"expect_health": ""}))
	if !out.Success {
		t.Fatalf("the health check was off and the probe still failed: %s", out.Message)
	}
	if !strings.Contains(out.Message, "unhealthy") || !strings.Contains(out.Message, "7 restart(s)") {
		t.Errorf("message %q should still report what the daemon said", out.Message)
	}
}

func TestDockerRunOnAStartingContainer(t *testing.T) {
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusOK,
		inspectBody("/nginx", "running", true, "starting", 0)))

	out := runDocker(t, dockerSensor(socket, nil))
	if out.Success {
		t.Fatalf("a starting container passed a healthy assertion: %s", out.Message)
	}
	if !strings.Contains(out.Message, "starting") || !strings.Contains(out.Message, "expected healthy") {
		t.Errorf("message %q should name both the state and the expectation", out.Message)
	}
}

func TestDockerRunExpectingAnUnhealthyContainer(t *testing.T) {
	// The expectation is a choice, not a hardcoded preference for green: a
	// sensor may watch for something that is supposed to be unhealthy.
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusOK,
		inspectBody("/nginx", "running", true, "unhealthy", 0)))

	out := runDocker(t, dockerSensor(socket, model.Options{"expect_health": "unhealthy"}))
	if !out.Success {
		t.Fatalf("unhealthy was expected and reported a failure: %s", out.Message)
	}
}

func TestDockerRunOnAStoppedContainer(t *testing.T) {
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusOK,
		inspectBody("/nginx", "exited", false, "", 0)))

	out := runDocker(t, dockerSensor(socket, model.Options{"expect_health": ""}))
	if out.Success {
		t.Fatalf("a stopped container was reported up: %s", out.Message)
	}
	if !strings.Contains(out.Message, "exited") || !strings.Contains(out.Message, "expected it to be running") {
		t.Errorf("message %q should name the state and the expectation", out.Message)
	}
}

func TestDockerRunExpectingAStoppedContainer(t *testing.T) {
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusOK,
		inspectBody("/nginx", "exited", false, "", 0)))

	out := runDocker(t, dockerSensor(socket,
		model.Options{"expect_health": "", "expect_running": false}))
	if !out.Success {
		t.Fatalf("a stopped container was expected to be stopped: %s", out.Message)
	}
}

// ---------------------------------------------------------------------------
// restarts
// ---------------------------------------------------------------------------

func TestDockerRunFailsAboveTheRestartLimit(t *testing.T) {
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusOK,
		inspectBody("/nginx", "running", true, "healthy", 5)))

	out := runDocker(t, dockerSensor(socket, model.Options{"max_restarts": 3}))
	if out.Success {
		t.Fatalf("five restarts passed a limit of three: %s", out.Message)
	}
	if !strings.Contains(out.Message, "restarted 5 time(s)") || !strings.Contains(out.Message, "limit is 3") {
		t.Errorf("message %q should give both the count and the limit", out.Message)
	}
}

func TestDockerRunAcceptsTheRestartLimitItself(t *testing.T) {
	// The limit is a ceiling, not a count that must stay below it: a container
	// that has restarted exactly as often as allowed is within it.
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusOK,
		inspectBody("/nginx", "running", true, "healthy", 3)))

	out := runDocker(t, dockerSensor(socket, model.Options{"max_restarts": 3}))
	if !out.Success {
		t.Fatalf("three restarts hit a limit of three: %s", out.Message)
	}
}

func TestDockerRunWithoutARestartLimitIgnoresTheCount(t *testing.T) {
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusOK,
		inspectBody("/nginx", "running", true, "healthy", 99)))

	out := runDocker(t, dockerSensor(socket, nil))
	if !out.Success {
		t.Fatalf("0 means the check is off, but the probe failed: %s", out.Message)
	}
}

// ---------------------------------------------------------------------------
// what the daemon can answer instead of a container
// ---------------------------------------------------------------------------

func TestDockerRunOnAMissingContainer(t *testing.T) {
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusNotFound,
		`{"message":"No such container: nginx"}`))

	out := runDocker(t, dockerSensor(socket, nil))
	if out.Success {
		t.Fatal("a 404 was reported as success")
	}
	if !strings.Contains(out.Message, `no container named "nginx"`) {
		t.Errorf("message %q should name the container it could not find", out.Message)
	}
	// The daemon's own sentence is worth passing on: it is the one that says
	// whether the name was wrong or the container is gone. It is read out of the
	// JSON so that the tooltip does not show braces and quotes.
	if !strings.Contains(out.Message, "No such container") {
		t.Errorf("message %q dropped the daemon's explanation", out.Message)
	}
	if strings.Contains(out.Message, `{"message"`) {
		t.Errorf("message %q shows the raw JSON body", out.Message)
	}
}

func TestDockerRunOnAnEngineErrorThatIsNotJSON(t *testing.T) {
	// A proxy, or a daemon answering while it dies, can put anything in the
	// body. The raw text is the fallback rather than an error about the error.
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusBadGateway,
		"<html><body>502 Bad Gateway</body></html>"))

	out := runDocker(t, dockerSensor(socket, nil))
	if out.Success {
		t.Fatal("a 502 was reported as success")
	}
	if !strings.Contains(out.Message, "502") || !strings.Contains(out.Message, "Bad Gateway") {
		t.Errorf("message %q should carry what the responder said", out.Message)
	}
}

func TestDockerRunOnAnEngineError(t *testing.T) {
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusInternalServerError,
		"something went wrong in the daemon"))

	out := runDocker(t, dockerSensor(socket, nil))
	if out.Success {
		t.Fatal("a 500 was reported as success")
	}
	if !strings.Contains(out.Message, "500") {
		t.Errorf("message %q should name the status it got", out.Message)
	}
}

func TestDockerRunOnAnAnswerThatIsNotAContainer(t *testing.T) {
	socket := dockerStub(t, (&requestLog{}).answer(http.StatusOK, "not json at all"))

	out := runDocker(t, dockerSensor(socket, nil))
	if out.Success {
		t.Fatal("garbage was reported as success")
	}
	if strings.TrimSpace(out.Message) == "" {
		t.Error("failure without a message")
	}
}

// ---------------------------------------------------------------------------
// the socket is the host's, so it is checked at probe time
// ---------------------------------------------------------------------------

func TestDockerValidateAcceptsASocketThatIsNotThereYet(t *testing.T) {
	// The socket belongs to the host. It can be absent while a sensor is being
	// written — the mount may be added later, or this may be a laptop with the
	// configuration copied from the server. Refusing the sensor would be refusing
	// a configuration that is fine; the probe is where the absence belongs.
	c := dockerSensor(absentSocket(t), nil)
	if err := (DockerProber{}).Validate(c); err != nil {
		t.Fatalf("a socket that does not exist yet was rejected at validation time: %v", err)
	}
}

func TestDockerRunOnAMissingSocket(t *testing.T) {
	socket := absentSocket(t)

	out := runDocker(t, dockerSensor(socket, nil))
	if out.Success {
		t.Fatal("a missing socket was reported as success")
	}
	if !strings.Contains(out.Message, "no Docker socket") ||
		!strings.Contains(out.Message, "mounted") {
		t.Errorf("message %q should say the socket is missing and ask about the mount", out.Message)
	}
}

func TestDockerRunOnASocketThatTimesOut(t *testing.T) {
	// A daemon that accepts the connection and then says nothing. The probe must
	// come back within its own deadline rather than hang the scheduler.
	socket := filepath.Join(shortTempDir(t), "d.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	out := DockerProber{}.Run(ctx, dockerSensor(socket, nil))
	if out.Success {
		t.Fatal("a silent socket was reported as success")
	}
	if !strings.Contains(out.Message, "timed out") {
		t.Errorf("message %q should say it timed out", out.Message)
	}
}

func TestDescribeDockerErrorNamesTheThreeAnswers(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		socket string
		want   string
	}{
		{
			"missing",
			&net.OpError{Op: "dial", Net: "unix", Err: os.ErrNotExist},
			"/var/run/docker.sock", "no Docker socket",
		},
		{
			"refused",
			&net.OpError{Op: "dial", Net: "unix", Err: os.ErrPermission},
			"/var/run/docker.sock", "permission denied",
		},
		{
			"deadline",
			&net.OpError{Op: "dial", Net: "unix", Err: context.DeadlineExceeded},
			"/var/run/docker.sock", "timed out",
		},
		{
			"anything else",
			&net.OpError{Op: "dial", Net: "unix", Err: fmt.Errorf("connection refused")},
			"/var/run/docker.sock", "connection refused",
		},
	}
	for _, tc := range cases {
		got := describeDockerError(tc.err, tc.socket)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q does not contain %q", tc.name, got, tc.want)
		}
		if !strings.Contains(got, tc.socket) {
			t.Errorf("%s: %q does not name the socket", tc.name, got)
		}
	}
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

func TestDockerValidate(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		options model.Options
		want    string
	}{
		{"no target", "", nil, "target is required"},
		{"a path in the target", "a/b", nil, "container name or id"},
		{"an escaped target", "a?b", nil, "container name or id"},
		{"an empty socket", "nginx", model.Options{"socket": ""}, "socket must not be empty"},
		{"a relative socket", "nginx", model.Options{"socket": "d.sock"}, "absolute path"},
		{"an unknown health", "nginx", model.Options{"expect_health": "green"}, "expect_health"},
		{"negative restarts", "nginx", model.Options{"max_restarts": -1}, "max_restarts"},
	}
	for _, tc := range cases {
		c := dockerSensor("/var/run/docker.sock", tc.options)
		c.Target = tc.target
		err := DockerProber{}.Validate(c)
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v does not mention %q", tc.name, err, tc.want)
		}
	}
}

func TestDockerValidateAcceptsTheDefaults(t *testing.T) {
	c := dockerSensor("/var/run/docker.sock", nil)
	if err := (DockerProber{}).Validate(c); err != nil {
		t.Fatalf("the default configuration was rejected: %v", err)
	}
}

func TestDockerType(t *testing.T) {
	if got := (DockerProber{}).Type(); got != model.SensorDocker {
		t.Errorf("Type() = %q, want %q", got, model.SensorDocker)
	}
}
