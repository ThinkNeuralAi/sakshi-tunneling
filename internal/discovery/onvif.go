// Package discovery finds cameras on the local network without needing
// the NVR login. It sends an ONVIF WS-Discovery probe (a SOAP multicast
// on UDP 239.255.255.250:3702) and collects the XAddrs each device
// advertises. This catches standalone cameras that aren't behind the
// recorder, and lets us cross-check the NVR's channel list.
//
// ONVIF is a discovery/control standard, not a transport: we use it to
// FIND devices, then still pull video over RTSP.
package discovery

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// Device is a single ONVIF responder on the LAN.
type Device struct {
	Address string // IP that answered
	XAddr   string // ONVIF service URL it advertised
}

var xaddrRe = regexp.MustCompile(`<[^>]*XAddrs>([^<]+)<`)
var ipRe = regexp.MustCompile(`https?://([0-9.]+)`)

// Probe multicasts a WS-Discovery Probe and returns responders seen
// within the timeout. Best-effort: discovery is a nicety, never a
// hard dependency, so callers should tolerate an empty result.
func Probe(ctx context.Context, timeout time.Duration) ([]Device, error) {
	const group = "239.255.255.250:3702"
	raddr, err := net.ResolveUDPAddr("udp4", group)
	if err != nil {
		return nil, err
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("open discovery socket: %w", err)
	}
	defer conn.Close()

	msg := probeMessage(uuid.NewString())
	if _, err := conn.WriteToUDP([]byte(msg), raddr); err != nil {
		return nil, fmt.Errorf("send probe: %w", err)
	}

	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(deadline)

	found := map[string]Device{}
	buf := make([]byte, 65535)
	for {
		if ctx.Err() != nil {
			break
		}
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			break // timeout or ctx cancel ends the loop
		}
		body := string(buf[:n])
		if m := xaddrRe.FindStringSubmatch(body); m != nil {
			xaddr := firstField(m[1])
			d := Device{Address: src.IP.String(), XAddr: xaddr}
			if ip := ipRe.FindStringSubmatch(xaddr); ip != nil {
				d.Address = ip[1]
			}
			found[d.Address] = d
		}
	}

	out := make([]Device, 0, len(found))
	for _, d := range found {
		out = append(out, d)
	}
	return out, nil
}

func firstField(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			return s[:i]
		}
	}
	return s
}

// probeMessage is the standard WS-Discovery SOAP envelope probing for
// any NetworkVideoTransmitter (the ONVIF device type for cameras).
func probeMessage(msgID string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope" ` +
		`xmlns:w="http://schemas.xmlsoap.org/ws/2004/08/addressing" ` +
		`xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" ` +
		`xmlns:dn="http://www.onvif.org/ver10/network/wsdl">` +
		`<e:Header>` +
		`<w:MessageID>uuid:` + msgID + `</w:MessageID>` +
		`<w:To e:mustUnderstand="true">urn:schemas-xmlsoap-org:ws:2005:04:discovery</w:To>` +
		`<w:Action e:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</w:Action>` +
		`</e:Header>` +
		`<e:Body><d:Probe><d:Types>dn:NetworkVideoTransmitter</d:Types></d:Probe></e:Body>` +
		`</e:Envelope>`
}
