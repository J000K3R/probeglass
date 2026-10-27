package mos

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDiscoverListsThePartsOfANAS(t *testing.T) {
	mux := http.NewServeMux()
	write := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		})
	}
	write("/api/v1/disks", `[{"name":"sda","type":"hdd","temperature":35},{"name":"sdb","type":"hdd","temperature":null},{"name":"sdz","type":"usb","removable":true,"temperature":null}]`)
	write("/api/v1/pools", `[{"id":"p1","name":"data","type":"btrfs"}]`)
	write("/api/v1/docker/mos/containers", `[{"name":"nginx","performance":{"cpu":{"usage":2.5}}}]`)
	write("/api/v1/lxc/containers", `[{"name":"adguard","state":"running"}]`)
	write("/api/v1/vm/machines", `[{"name":"ubuntu","state":"stopped"}]`)
	write("/api/v1/mos/sensors", `{"temperature":[{"name":"CPU","value":45,"unit":"C"}]}`)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	checks, notes, err := Discover(ctx, strings.TrimPrefix(srv.URL, "http://"), "test-token")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("discovery notes: %v", notes)
	}

	byKey := map[string]Check{}
	for _, check := range checks {
		byKey[check.Key] = check
	}
	for _, key := range []string{
		"disk.sda.temperature", "disk.sda.smart",
		"disk.sdb.smart",
		"pool.p1.mounted", "docker.nginx.running", "lxc.adguard.state",
		"vm.ubuntu.state", "sensor.temperature.CPU",
	} {
		if _, ok := byKey[key]; !ok {
			t.Errorf("discovery has no %q; it has %v", key, checks)
		}
	}
	if got := byKey["disk.sda.temperature"]; got.Options["json_match_key"] != "name" ||
		got.Options["json_path"] != "temperature" {
		t.Errorf("the disk temperature check is not a matched JSON read: %v", got.Options)
	}
	if got := byKey["lxc.adguard.state"]; got.Target != "http://{address}/api/v1/lxc/containers?performance=true" {
		t.Errorf("the LXC check target is %q", got.Target)
	}
	if _, ok := byKey["disk.sdz.smart"]; ok {
		t.Error("a removable USB disk produced a disk sensor")
	}
	if len(checks) != 8 {
		t.Errorf("discovery returned %d checks, want 8", len(checks))
	}
}
