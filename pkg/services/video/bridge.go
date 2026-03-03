package video

import (
	"context"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"
	"github.com/Constellation-Overwatch/pulsar/pkg/shared"
)

// StartBridge publishes video sources to the RTSP server via ffmpeg subprocesses.
// For each entity with a video source (RTSP URL or device), it spawns an ffmpeg
// process that reads from the source and publishes to the RTSP server at
// /{entity_id}. This makes the raw stream available on MediaMTX for both
// downstream consumers and the detector.
//
// Returns a cleanup function that stops all publishers.
func StartBridge(ctx context.Context, state *shared.C4State, srv *RTSPServer) func() {
	if srv.Mode() == "none" {
		logger.Info("[video] no RTSP server available, skipping video bridge")
		return func() {}
	}

	var publishers []*SourcePublisher
	bridged := 0

	for _, entity := range state.Entities {
		source := resolveSource(entity)
		if source == "" {
			continue
		}

		pub := StartSourcePublisher(ctx, source, entity.EntityID, srv.rtspHost, srv.rtspPort)
		publishers = append(publishers, pub)
		bridged++
	}

	if bridged > 0 {
		logger.Infof("[bridge] publishing %d source(s) to %s RTSP server", bridged, srv.Mode())
	}

	return func() {
		for _, p := range publishers {
			p.Close()
		}
		if bridged > 0 {
			logger.Info("[bridge] all source publishers stopped")
		}
	}
}

// resolveSource returns the video source for an entity.
// Checks video_config["source"] (RTSP URL) first, then video_config["device"].
func resolveSource(entity shared.EntityState) string {
	if entity.VideoConfig == nil {
		return ""
	}
	if src, ok := entity.VideoConfig["source"].(string); ok && src != "" {
		return src
	}
	if dev, ok := entity.VideoConfig["device"].(string); ok && dev != "" {
		return dev
	}
	return ""
}
