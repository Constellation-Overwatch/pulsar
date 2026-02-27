package video

import (
	"context"
	"strings"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"
	"github.com/Constellation-Overwatch/pulsar/pkg/shared"
)

// StartBridge configures video source proxying for all entities with video sources.
// In MediaMTX mode, it configures source proxy paths via the API.
// In embedded mode, RTSP sources are read directly by the detector (no proxy needed).
//
// For each entity with a video source, it also configures the overlay path
// ({entity_id}/pulsar) for the detector to publish annotated frames.
//
// Returns a cleanup function that removes configured paths.
func StartBridge(ctx context.Context, state *shared.C4State, srv *RTSPServer) func() {
	if srv.Mode() == "none" {
		logger.Info("[video] no RTSP server available, skipping video bridge")
		return func() {}
	}

	bridged := 0

	for i, entity := range state.Entities {
		source := resolveVideoSource(entity)

		switch srv.Mode() {
		case "mediamtx":
			if source != "" {
				// MediaMTX auto-creates paths when clients publish/connect.
				// Just track which sources are bridged for logging.
				logger.Infof("[video] %s: source %s -> /%s (mediamtx)", entity.Name, source, entity.EntityID)
				bridged++
			}

		case "embedded":
			if source != "" && strings.HasPrefix(source, "rtsp://") {
				// Embedded mode: detector reads directly from source URL
				// Update the entity's RTSPURL so the detector knows where to read
				state.Entities[i].RTSPURL = source
				logger.Infof("[video] %s: detector will read directly from %s (embedded mode)", entity.Name, source)
				bridged++
			}
			// For device sources, bridge_device.go handles capture → embedded server
			// Overlay path is implicit on embedded server (created on first ANNOUNCE)
		}
	}

	if bridged > 0 {
		logger.Infof("[video] bridge configured %d source(s) (%s mode)", bridged, srv.Mode())
	}

	// Start device captures (gocv-dependent, behind detection build tag)
	deviceCleanup := startDeviceCaptures(ctx, state.Entities, srv)

	return func() {
		deviceCleanup()
		logger.Infof("[video] bridge cleaned up")
	}
}

// resolveVideoSource extracts the source URL from entity video_config.
// Priority: video_config["source"] > empty (device handled separately)
func resolveVideoSource(entity shared.EntityState) string {
	if entity.VideoConfig == nil {
		return ""
	}
	if src, ok := entity.VideoConfig["source"].(string); ok && src != "" {
		return src
	}
	return ""
}
