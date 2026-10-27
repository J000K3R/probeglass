package sensor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/j000k3r/probeglass/internal/model"
)

// Checking a device before a template is applied. It answers the two questions
// that decide whether the sensors will work at all: can this address be reached
// with these credentials, and what does the box say it is?

const (
	sysDescrOID    = "1.3.6.1.2.1.1.1.0"
	sysObjectIDOID = "1.3.6.1.2.1.1.2.0"
)

// Discovery is what one device said about itself.
type Discovery struct {
	SysDescr    string `json:"sys_descr"`
	SysObjectID string `json:"sys_object_id"`
}

// DiscoverSNMP reads the device description and object id.
//
// This is a real SNMP exchange with the credentials the template is about to be
// applied with, not a validation of the form. A device that cannot be reached,
// or that refuses the community or the v3 user, fails here — before a set of
// sensors is created that could only ever be red.
func DiscoverSNMP(ctx context.Context, target string, options model.Options) (Discovery, error) {
	host, port, err := snmpTarget(target)
	if err != nil {
		return Discovery{}, err
	}
	resolved, err := resolveSNMPHost(ctx, host)
	if err != nil {
		return Discovery{}, fmt.Errorf("resolve %s: %s", host, describeRequestError(err))
	}

	version := gosnmp.Version2c
	switch strings.ToLower(options.Str("version", "2c")) {
	case "1":
		version = gosnmp.Version1
	case "3":
		version = gosnmp.Version3
	}
	timeout := 10 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); left > 0 && left < timeout {
			timeout = left
		}
	}

	client := &gosnmp.GoSNMP{
		Target:    resolved.IP.String(),
		Port:      port,
		Version:   version,
		Transport: "udp",
		Timeout:   timeout,
		Retries:   0,
		MaxOids:   4,
		Context:   ctx,
	}
	applySNMPSecurity(client, options, version)
	if err := client.Connect(); err != nil {
		return Discovery{}, fmt.Errorf("%s", describeSNMPError(err, target, timeout))
	}
	defer func() { _ = client.Conn.Close() }()

	packet, err := client.Get([]string{sysDescrOID, sysObjectIDOID})
	if err != nil {
		return Discovery{}, fmt.Errorf("%s", describeSNMPError(err, target, timeout))
	}
	var out Discovery
	for _, pdu := range packet.Variables {
		text, _, _ := snmpValue(pdu)
		switch normaliseOID(pdu.Name) {
		case sysDescrOID:
			out.SysDescr = text
		case sysObjectIDOID:
			out.SysObjectID = strings.TrimPrefix(text, ".")
		}
	}
	return out, nil
}
