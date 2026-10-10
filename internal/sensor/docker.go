package sensor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
)

// DockerProber watches one container on the host the daemon runs on.
//
// It speaks to the Docker Engine API directly over the unix socket, with the
// standard library and nothing else: the API is HTTP, the socket is a stream,
// and a Transport with its own dialer is the whole of the bridge. No client
// library, no sidecar.
//
// It only ever issues a GET, and only ever for the one container it is pointed
// at. Nothing here starts, stops or changes a container.
//
// Options:
//
//	socket          path to the Docker socket, default /var/run/docker.sock
//	expect_running  default true; false fails a container that is up
//	expect_health   expected health status: healthy, starting or unhealthy.
//	                Default healthy. Empty switches the check off.
//	max_restarts    fail when the restart count is above this. 0 switches the
//	                check off.
type DockerProber struct{}

// Type implements Prober.
func (DockerProber) Type() model.SensorType { return model.SensorDocker }

// defaultDockerSocket is where the daemon's socket normally is, both on a host
// and inside a container with the socket mounted.
const defaultDockerSocket = "/var/run/docker.sock"

// maxInspectBytes bounds what is read from the daemon. A container's inspect
// output is a few kilobytes; anything past this is not a response worth
// parsing.
const maxInspectBytes = 1 << 20

// Validate implements Prober.
func (DockerProber) Validate(c model.Sensor) error {
	target := strings.TrimSpace(c.Target)
	if target == "" {
		return errors.New("target is required: a container name or id")
	}
	if strings.ContainsAny(target, "/?#") {
		return fmt.Errorf("target must be a container name or id, got %q", target)
	}
	if socket := strings.TrimSpace(c.Options.Str("socket", defaultDockerSocket)); socket == "" {
		return errors.New("socket must not be empty")
	} else if !strings.HasPrefix(socket, "/") {
		return fmt.Errorf("socket must be an absolute path, got %q", socket)
	}
	switch health := c.Options.Str("expect_health", "healthy"); health {
	case "", "healthy", "starting", "unhealthy":
	default:
		return fmt.Errorf("expect_health must be healthy, starting or unhealthy, got %q", health)
	}
	if n := c.Options.Int("max_restarts", 0); n < 0 {
		return errors.New("max_restarts must not be negative")
	}
	return nil
}

// dockerInspect is the part of GET /containers/{id}/json this sensor reads.
//
// Health is a pointer because the daemon omits it entirely for a container with
// no HEALTHCHECK — which is most of them — and "no health check configured" is
// a different thing from "the health check is unhappy".
type dockerInspect struct {
	Name  string `json:"Name"`
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
		Health  *struct {
			Status        string `json:"Status"`
			FailingStreak int    `json:"FailingStreak"`
		} `json:"Health"`
	} `json:"State"`
	RestartCount int `json:"RestartCount"`
}

// Run implements Prober.
func (DockerProber) Run(ctx context.Context, c model.Sensor) Outcome {
	o := c.Options
	target := strings.TrimSpace(c.Target)
	socket := strings.TrimSpace(o.Str("socket", defaultDockerSocket))

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	// The host in the URL is a placeholder: the dialer decides where the
	// connection goes, and the daemon ignores it.
	endpoint := "http://docker/containers/" + url.PathEscape(target) + "/json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Errf(0, "build request: %v", err)
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return Errf(latency, "%s", describeDockerError(err, socket))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The daemon explains its refusals in the body, and "no such container"
		// is the one an operator will actually see. The body is JSON, but a
		// proxy or a half-dead daemon can put anything there, so the raw text is
		// the fallback rather than an error of its own.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		detail := dockerAPIMessage(body)
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		if resp.StatusCode == http.StatusNotFound {
			return Errf(latency, "no container named %q: %s", target, detail)
		}
		return Errf(latency, "the Docker API answered %d: %s", resp.StatusCode, truncate(detail, 120))
	}

	var info dockerInspect
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInspectBytes))
	if err != nil {
		return Errf(latency, "read the inspect response: %v", err)
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return Errf(latency, "the Docker API returned something that is not a container: %v", err)
	}

	name := strings.TrimPrefix(info.Name, "/")
	if name == "" {
		name = target
	}

	// Restarts are always reported, whether or not a limit is set: the number is
	// what tells somebody the limit is worth setting. It belongs in the message
	// and not in StatusCode, which is a protocol value everywhere else.
	restarts := fmt.Sprintf("%d restart(s)", info.RestartCount)
	health := ""
	if info.State.Health != nil {
		health = info.State.Health.Status
	}

	// --- the assertions ------------------------------------------------------

	if want := o.Bool("expect_running", true); want != info.State.Running {
		if want {
			return Errf(latency, "container %s is %s, expected it to be running",
				name, dockerStateText(info.State.Status))
		}
		return Errf(latency, "container %s is running, expected it to be stopped", name)
	}

	if want := o.Str("expect_health", "healthy"); want != "" {
		if info.State.Health == nil {
			// Naming the option is the difference between a reading and an
			// instruction: this container has no HEALTHCHECK, so no choice of
			// status could ever satisfy the sensor, and the only fix is here.
			return Errf(latency, "container %s has no health check configured, so it can never "+
				"report %q — clear expect_health to check only that the container is running",
				name, want)
		}
		if health != want {
			return Errf(latency, "container %s is %s, expected %s", name, health, want)
		}
	}

	if limit := o.Int("max_restarts", 0); limit > 0 && info.RestartCount > limit {
		return Errf(latency, "container %s has restarted %d time(s), the limit is %d",
			name, info.RestartCount, limit)
	}

	// --- the success line ----------------------------------------------------

	parts := []string{name, dockerStateText(info.State.Status)}
	if health != "" {
		parts = append(parts, health)
	}
	parts = append(parts, restarts)
	parts = append(parts, fmt.Sprintf("%.0fms", float64(latency.Microseconds())/1000))
	return Outcome{
		Success: true,
		Latency: latency,
		Message: strings.Join(parts, " · "),
	}
}

// dockerStateText makes a status the daemon left empty readable.
func dockerStateText(status string) string {
	if strings.TrimSpace(status) == "" {
		return "in an unknown state"
	}
	return status
}

// dockerAPIMessage pulls the daemon's own sentence out of an error body. The
// Engine API answers a refusal with {"message":"..."}, and reading it out keeps
// braces and quotes out of a dashboard tooltip.
func dockerAPIMessage(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err == nil {
		if msg := strings.TrimSpace(e.Message); msg != "" {
			return msg
		}
	}
	return strings.TrimSpace(string(body))
}

// describeDockerError turns a failure to reach the socket into something that
// says what to do about it.
//
// The socket is the one dependency of this sensor that lives outside the
// process, and its three failures are all configuration mistakes with three
// different answers: it was never mounted, the daemon may not read it, or the
// daemon it belongs to is not answering. A generic "connection failed" would
// help with none of them.
func describeDockerError(err error, socket string) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("no Docker socket at %s — is it mounted into this container?", socket)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Sprintf("permission denied on %s — this container may not read it", socket)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("timed out talking to the Docker socket %s", socket)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("timed out talking to the Docker socket %s", socket)
	}
	// Something else went wrong on the way, most often a socket file with
	// nothing behind it. The shared wording says what it was; the name of the
	// socket says where.
	return fmt.Sprintf("talking to the Docker socket %s: %s", socket, describeRequestError(err))
}
