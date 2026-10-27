// Package mos talks to the MOS NAS OS API for one purpose: to find the things a
// NAS actually has — disks, pools, containers, VMs and hardware sensors — and
// turn each into a sensor. It does not try to be a general MOS client.
package mos

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// Check is one discovered sensor, shaped like the catalogue's checks so the
// dialog can show and create it without a second code path.
type Check struct {
	Key      string           `json:"key"`
	Label    string           `json:"label"`
	Type     model.SensorType `json:"type"`
	Target   string           `json:"target"`
	Interval int              `json:"interval"`
	Options  model.Options    `json:"options"`
}

type disk struct {
	Name        string   `json:"name"`
	Model       string   `json:"model"`
	Temperature *float64 `json:"temperature"`
}

type pool struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type container struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type sensorValue struct {
	Name  string   `json:"name"`
	Value *float64 `json:"value"`
	Unit  string   `json:"unit"`
}

// Discover lists the parts of one MOS installation. A list endpoint that fails is
// a note and not the end of the discovery: a NAS with no VMs is normal, and a
// token without permission for one section should not hide the disks.
func Discover(ctx context.Context, address, token string) ([]Check, []string, error) {
	base := "http://" + cleanAddress(address) + "/api/v1"
	client := &http.Client{Timeout: 15 * time.Second}
	var checks []Check
	var notes []string

	var disks []disk
	if err := get(ctx, client, base+"/disks?performance=true", token, &disks); err != nil {
		return nil, nil, err
	}
	for _, d := range disks {
		name := d.Name
		if name == "" {
			continue
		}
		checks = append(checks,
			Check{
				Key:   "disk." + safe(name) + ".temperature",
				Label: fmt.Sprintf("Disk %s temperature", name),
				Type:  model.SensorHTTP, Target: "http://{address}/api/v1/disks?performance=true",
				Interval: 60,
				Options: model.Options{
					"json_match_key": "name", "json_match_value": name,
					"json_path": "temperature", "json_max": 55,
				},
			},
			Check{
				Key:   "disk." + safe(name) + ".smart",
				Label: fmt.Sprintf("Disk %s SMART status", name),
				Type:  model.SensorHTTP, Target: "http://{address}/api/v1/disks/" + url.PathEscape(name) + "/smart",
				Interval: 300,
				Options:  model.Options{"json_path": "smartStatus", "json_expect": "PASSED"},
			},
		)
	}

	var pools []pool
	if err := get(ctx, client, base+"/pools", token, &pools); err != nil {
		notes = append(notes, "pools: "+err.Error())
	}
	for _, p := range pools {
		if p.ID == "" {
			continue
		}
		label := p.Name
		if label == "" {
			label = p.ID
		}
		checks = append(checks, Check{
			Key: "pool." + safe(p.ID) + ".mounted", Label: fmt.Sprintf("Pool %s mounted", label),
			Type: model.SensorHTTP, Target: "http://{address}/api/v1/pools/" + url.PathEscape(p.ID) + "/status",
			Interval: 60, Options: model.Options{"json_path": "mounted", "json_expect": "true"},
		})
	}

	var docker []container
	if err := get(ctx, client, base+"/docker/mos/containers?performance=true", token, &docker); err != nil {
		notes = append(notes, "docker: "+err.Error())
	}
	for _, c := range docker {
		if c.Name == "" {
			continue
		}
		checks = append(checks, Check{
			Key: "docker." + safe(c.Name) + ".cpu", Label: fmt.Sprintf("Docker %s CPU", c.Name),
			Type: model.SensorHTTP, Target: "http://{address}/api/v1/docker/mos/containers?performance=true",
			Interval: 60,
			Options: model.Options{
				"json_match_key": "name", "json_match_value": c.Name,
				"json_path": "performance.cpu.usage", "json_min": 0,
			},
		})
	}

	var lxc []container
	if err := get(ctx, client, base+"/lxc/containers?performance=true", token, &lxc); err != nil {
		notes = append(notes, "lxc: "+err.Error())
	}
	for _, c := range lxc {
		if c.Name == "" {
			continue
		}
		checks = append(checks, Check{
			Key: "lxc." + safe(c.Name) + ".state", Label: fmt.Sprintf("LXC %s running", c.Name),
			Type: model.SensorHTTP, Target: "http://{address}/api/v1/lxc/containers?performance=true",
			Interval: 60,
			Options: model.Options{
				"json_match_key": "name", "json_match_value": c.Name,
				"json_path": "state", "json_expect": "running",
			},
		})
	}

	var vms []container
	if err := get(ctx, client, base+"/vm/machines?performance=true", token, &vms); err != nil {
		notes = append(notes, "vm: "+err.Error())
	}
	for _, v := range vms {
		if v.Name == "" {
			continue
		}
		checks = append(checks, Check{
			Key: "vm." + safe(v.Name) + ".state", Label: fmt.Sprintf("VM %s running", v.Name),
			Type: model.SensorHTTP, Target: "http://{address}/api/v1/vm/machines?performance=true",
			Interval: 60,
			Options: model.Options{
				"json_match_key": "name", "json_match_value": v.Name,
				"json_path": "state", "json_expect": "running",
			},
		})
	}

	var sensors map[string][]sensorValue
	if err := get(ctx, client, base+"/mos/sensors", token, &sensors); err != nil {
		notes = append(notes, "sensors: "+err.Error())
	}
	for group, list := range sensors {
		for index, sensor := range list {
			if sensor.Name == "" {
				continue
			}
			options := model.Options{"json_path": fmt.Sprintf("%s.%d.value", group, index)}
			switch group {
			case "temperature":
				options["json_max"] = 85
			default:
				options["json_min"] = 0
			}
			label := sensor.Name
			if sensor.Unit != "" {
				label += " (" + sensor.Unit + ")"
			}
			checks = append(checks, Check{
				Key: "sensor." + safe(group) + "." + safe(sensor.Name), Label: label,
				Type: model.SensorHTTP, Target: "http://{address}/api/v1/mos/sensors",
				Interval: 60, Options: options,
			})
		}
	}

	return checks, notes, nil
}

func get(ctx context.Context, client *http.Client, url, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	// The address is the NAS an administrator typed into the template; making
	// that request is the discovery. Refusing private addresses would make it
	// unable to find the homelab it is for.
	// codeql[go/request-forgery]
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

func cleanAddress(address string) string {
	a := strings.TrimSpace(address)
	a = strings.TrimPrefix(a, "http://")
	a = strings.TrimPrefix(a, "https://")
	if i := strings.IndexByte(a, '/'); i >= 0 {
		a = a[:i]
	}
	return a
}

func safe(name string) string {
	r := strings.NewReplacer(" ", "-", "/", "-", ":", "-", "\"", "", "'", "")
	return r.Replace(name)
}
