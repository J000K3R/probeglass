package api

import (
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/notify"
)

// secretOptionKeys lists the sensor options that hold credentials. They are
// replaced by notify.StoredMarker on the way out and restored from the stored
// sensor on the way back in, so a credential the dashboard is allowed to keep
// but never has to display.
//
// A sensor's own options are a lower-risk surface than a channel's config — the
// dashboard user is the administrator — but probe credentials are commonly
// reused elsewhere (a router password, a registry token), so they get the same
// treatment.
var secretOptionKeys = map[model.SensorType][]string{
	model.SensorHTTP: {"bearer_token", "basic_auth_pass"},
	// SNMP credentials are shared passwords, and they are the only thing standing
	// between a stranger on the LAN and a device's whole MIB. A v2c community
	// travels unencrypted; a v3 passphrase is the key to an authenticated session.
	// There is no reason for either to sit in clear text in an API response.
	model.SensorSNMP: {"community", "auth_pass", "priv_pass"},
}

func sensorSecretKeys(t model.SensorType) []string { return secretOptionKeys[t] }

// maskSensor hides the credentials in a sensor's options.
func maskSensor(c model.Sensor) model.Sensor {
	keys := sensorSecretKeys(c.Type)
	if len(keys) == 0 || len(c.Options) == 0 {
		return c
	}
	options := make(model.Options, len(c.Options))
	for k, v := range c.Options {
		options[k] = v
	}
	for _, key := range keys {
		if s, ok := options[key].(string); ok && s != "" {
			options[key] = notify.StoredMarker
		}
	}
	c.Options = options
	return c
}

// maskSensors maps maskSensor over a slice.
func maskSensors(sensors []model.Sensor) []model.Sensor {
	out := make([]model.Sensor, len(sensors))
	for i, c := range sensors {
		out[i] = maskSensor(c)
	}
	return out
}

// unmaskSensor swaps the marker back for the value already stored. An empty
// value is honoured as a deliberate clear.
func unmaskSensor(incoming, existing model.Sensor) model.Sensor {
	keys := sensorSecretKeys(incoming.Type)
	if len(keys) == 0 {
		return incoming
	}
	if incoming.Options == nil {
		incoming.Options = model.Options{}
	}
	for _, key := range keys {
		if s, ok := incoming.Options[key].(string); ok && s == notify.StoredMarker {
			if previous, ok := existing.Options[key].(string); ok {
				incoming.Options[key] = previous
			} else {
				delete(incoming.Options, key)
			}
		}
	}
	return incoming
}
