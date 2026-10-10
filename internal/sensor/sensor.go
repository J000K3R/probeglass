// Package sensor contains the probe engines. Every sensor type implements
// Prober and registers itself in registry, which is the only place that needs
// editing to add a new type.
//
// A prober must never panic and must always return an Outcome; failures are
// data, not exceptions. The scheduler turns an Outcome into a stored result.
package sensor

import (
	"context"
	"fmt"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
)

// Outcome is the result of a single probe.
type Outcome struct {
	Success bool
	Latency time.Duration
	// StatusCode is the protocol-level status where one exists (HTTP status,
	// DNS answer count). Zero when not applicable.
	StatusCode int
	// Message is a short human-readable summary shown in the dashboard.
	Message string
	// Err describes why the probe failed. It is nil on success.
	Err error
}

// Failed reports whether the probe failed.
func (o Outcome) Failed() bool { return !o.Success }

// Errf builds a failed outcome with a formatted error message.
func Errf(latency time.Duration, format string, args ...any) Outcome {
	msg := fmt.Sprintf(format, args...)
	return Outcome{Success: false, Latency: latency, Message: msg, Err: fmt.Errorf("%s", msg)}
}

// Prober is implemented by every probe type.
type Prober interface {
	// Type reports which sensor type this prober handles.
	Type() model.SensorType
	// Validate reports whether the sensor's configuration is usable. It runs
	// before a sensor is saved, so users get immediate feedback.
	Validate(c model.Sensor) error
	// Run performs one probe. It must respect ctx and must not panic.
	Run(ctx context.Context, c model.Sensor) Outcome
}

// registry maps a sensor type to its implementation.
var registry = map[model.SensorType]Prober{
	model.SensorHTTP:   HTTPProber{},
	model.SensorTCP:    TCPProber{},
	model.SensorPing:   PingProber{},
	model.SensorDNS:    DNSProber{},
	model.SensorTLS:    TLSProber{},
	model.SensorDocker: DockerProber{},
	model.SensorSNMP:   SNMPProber{},
}

// Get returns the prober for a type.
func Get(t model.SensorType) (Prober, bool) {
	c, ok := registry[t]
	return c, ok
}

// Validate runs the prober's own validation plus the rules that apply to every
// type. The API calls this before persisting a sensor.
func Validate(c model.Sensor) error {
	if c.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(c.Name) > 120 {
		return fmt.Errorf("name must be at most 120 characters")
	}
	ck, ok := Get(c.Type)
	if !ok {
		return fmt.Errorf("unsupported sensor type %q", c.Type)
	}
	if c.Interval < 5 {
		return fmt.Errorf("interval must be at least 5 seconds")
	}
	if c.Interval > 86400 {
		return fmt.Errorf("interval must be at most 24 hours")
	}
	if c.Timeout < 1 {
		return fmt.Errorf("timeout must be at least 1 second")
	}
	if c.Timeout > 600 {
		return fmt.Errorf("timeout must be at most 600 seconds")
	}
	if c.Timeout > c.Interval*2 {
		return fmt.Errorf("timeout (%ds) should not exceed twice the interval (%ds)", c.Timeout, c.Interval)
	}
	if c.Retries < 1 {
		return fmt.Errorf("retries must be at least 1")
	}
	if c.Retries > 100 {
		return fmt.Errorf("retries must be at most 100")
	}
	if c.ReminderEvery < 0 {
		return fmt.Errorf("reminder interval must not be negative")
	}
	return ck.Validate(c)
}

// Test runs a sensor once outside the scheduler, for the "Test" button in the
// dashboard. It applies a sane timeout when the sensor has none yet.
func Test(ctx context.Context, c model.Sensor) Outcome {
	ck, ok := Get(c.Type)
	if !ok {
		return Errf(0, "unsupported sensor type %q", c.Type)
	}
	timeout := time.Duration(c.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return ck.Run(ctx, c)
}
