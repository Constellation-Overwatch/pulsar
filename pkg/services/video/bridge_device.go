//go:build detection

package video

import (
	"context"

	"github.com/Constellation-Overwatch/pulsar/pkg/shared"
)

// startDeviceCaptures is a no-op when the detection build is active.
// The detector opens device cameras directly to avoid the
// camera → H264 → RTSP → GoCV-decode round-trip. See detector.go.
func startDeviceCaptures(_ context.Context, _ []shared.EntityState, _ *RTSPServer) func() {
	return func() {}
}

// ResolveDeviceSource returns the device path from video_config, or empty.
// Exported so the detector can use it.
func ResolveDeviceSource(entity shared.EntityState) string {
	if entity.VideoConfig == nil {
		return ""
	}
	if src, ok := entity.VideoConfig["source"].(string); ok && src != "" {
		return ""
	}
	if dev, ok := entity.VideoConfig["device"].(string); ok && dev != "" {
		return dev
	}
	return ""
}

const (
	captureFPS    = 15   // target capture frame rate
	captureWidth  = 1280
	captureHeight = 720
)
