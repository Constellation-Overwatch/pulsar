package shared

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"
)

// NetworkInterface represents a discovered NIC with its addresses.
type NetworkInterface struct {
	Name  string   // e.g. "en0", "eth0"
	Addrs []string // IPv4 addresses
}

// DiscoverInterfaces returns all non-loopback network interfaces with IPv4 addresses.
func DiscoverInterfaces() []NetworkInterface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var result []NetworkInterface
	for _, iface := range ifaces {
		// Skip down, loopback, and point-to-point interfaces
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		var ipv4s []string
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil {
				continue // skip IPv6
			}
			// Skip link-local (169.254.x.x)
			if ip[0] == 169 && ip[1] == 254 {
				continue
			}
			ipv4s = append(ipv4s, ip.String())
		}

		if len(ipv4s) > 0 {
			result = append(result, NetworkInterface{
				Name:  iface.Name,
				Addrs: ipv4s,
			})
		}
	}

	// Sort: prefer en0/eth0 (primary) first
	sort.Slice(result, func(i, j int) bool {
		return nicPriority(result[i].Name) < nicPriority(result[j].Name)
	})

	return result
}

// PickAdvertiseHost selects the best IP for external advertisement.
// Priority: ADVERTISE_HOST env override > first non-loopback IPv4.
// Logs all available interfaces for visibility.
func PickAdvertiseHost(envOverride string) string {
	ifaces := DiscoverInterfaces()

	if len(ifaces) > 0 {
		logger.Info("[network] available interfaces:")
		for _, iface := range ifaces {
			logger.Infof("[network]   %s: %s", iface.Name, strings.Join(iface.Addrs, ", "))
		}
	}

	if envOverride != "" {
		logger.Infof("[network] using ADVERTISE_HOST override: %s", envOverride)
		return envOverride
	}

	if len(ifaces) == 0 {
		logger.Info("[network] no non-loopback interfaces found, using localhost")
		return "localhost"
	}

	selected := ifaces[0].Addrs[0]
	logger.Infof("[network] auto-selected advertise host: %s (%s)", selected, ifaces[0].Name)
	return selected
}

// FormatVideoEndpoints builds the externally-consumable video URLs for an entity.
// WebRTC and HLS point to the /pulsar overlay (post-inference annotated stream).
// When https is true, WebRTC/HLS URLs use https:// and rtsps:// schemes.
func FormatVideoEndpoints(advertiseHost string, rtspPort int, entityID string, https bool) map[string]interface{} {
	httpScheme := "http"
	if https {
		httpScheme = "https"
	}

	base := fmt.Sprintf("%s:%d/%s", advertiseHost, rtspPort, entityID)
	return map[string]interface{}{
		"protocol":    "rtsp",
		"port":        rtspPort,
		"stream_url":  fmt.Sprintf("rtsp://%s", base),
		"overlay_url": fmt.Sprintf("rtsp://%s/pulsar", base),
		"webrtc_url":  fmt.Sprintf("%s://%s:8889/%s/pulsar", httpScheme, advertiseHost, entityID),
		"hls_url":     fmt.Sprintf("%s://%s:8888/%s/pulsar", httpScheme, advertiseHost, entityID),
	}
}

// DeriveNATSURL extracts the hostname from a base HTTP URL and returns nats://host:4222.
// Falls back to nats://localhost:4222 on parse failure.
func DeriveNATSURL(baseURL string) string {
	fallback := "nats://localhost:4222"
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return fallback
	}
	return fmt.Sprintf("nats://%s:4222", u.Hostname())
}

// nicPriority returns a sort key — lower is higher priority.
func nicPriority(name string) int {
	switch {
	case strings.HasPrefix(name, "en0"), strings.HasPrefix(name, "eth0"):
		return 0 // primary wired/wifi
	case strings.HasPrefix(name, "en"), strings.HasPrefix(name, "eth"):
		return 1 // other ethernet
	case strings.HasPrefix(name, "wl"):
		return 2 // wireless (linux)
	case strings.HasPrefix(name, "utun"), strings.HasPrefix(name, "tun"), strings.HasPrefix(name, "wg"):
		return 3 // VPN
	case strings.HasPrefix(name, "bridge"), strings.HasPrefix(name, "docker"), strings.HasPrefix(name, "br-"):
		return 5 // container bridges
	default:
		return 4
	}
}
