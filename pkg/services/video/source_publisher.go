package video

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"
)

// SourcePublisher manages an ffmpeg subprocess that publishes a video source
// (RTSP URL or local device) to a MediaMTX RTSP path. It automatically
// restarts the ffmpeg process if it crashes.
type SourcePublisher struct {
	cancel context.CancelFunc
}

// StartSourcePublisher spawns an ffmpeg process that reads from the given source
// and publishes it to rtsp://{rtspHost}:{rtspPort}/{entityID}.
// For RTSP sources, ffmpeg does codec copy (no re-encoding).
// For device sources, ffmpeg encodes with libx264.
// The publisher restarts automatically if ffmpeg exits unexpectedly.
func StartSourcePublisher(ctx context.Context, source, entityID, rtspHost string, rtspPort int) *SourcePublisher {
	pubCtx, pubCancel := context.WithCancel(ctx)
	rtspOutput := fmt.Sprintf("rtsp://%s:%d/%s", rtspHost, rtspPort, entityID)

	go runSourceLoop(pubCtx, source, rtspOutput, entityID)

	return &SourcePublisher{cancel: pubCancel}
}

func runSourceLoop(ctx context.Context, source, rtspOutput, entityID string) {
	const maxBackoff = 10 * time.Second
	attempt := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		args := buildSourcePublishArgs(source, rtspOutput)
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)

		logger.Infof("[bridge] %s: ffmpeg publishing %s -> %s", entityID, source, rtspOutput)

		err := cmd.Run()
		if ctx.Err() != nil {
			return // context cancelled, clean shutdown
		}

		attempt++
		backoff := time.Duration(attempt) * 2 * time.Second
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		if attempt <= 3 || attempt%10 == 0 {
			logger.Warnf("[bridge] %s: ffmpeg exited (%v), restarting in %s (attempt %d)", entityID, err, backoff, attempt)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// buildSourcePublishArgs constructs ffmpeg arguments to publish a source to an RTSP URL.
// RTSP sources use codec copy (no re-encoding). Device sources use libx264.
func buildSourcePublishArgs(source, rtspOutput string) []string {
	if strings.HasPrefix(source, "rtsp://") {
		// RTSP source: passthrough codecs, no re-encoding
		return []string{
			"-v", "warning",
			"-rtsp_transport", "tcp",
			"-i", source,
			"-c", "copy",
			"-f", "rtsp",
			"-rtsp_transport", "tcp",
			rtspOutput,
		}
	}

	// Device source: needs encoding
	var inputArgs []string
	switch runtime.GOOS {
	case "darwin":
		// macOS: avfoundation — request 1280x720 natively if available,
		// capture at 30fps. The output will be scaled and rate-limited.
		inputArgs = []string{
			"-v", "warning",
			"-f", "avfoundation",
			"-video_size", fmt.Sprintf("%dx%d", captureWidth, captureHeight),
			"-framerate", "30",
			"-i", source + ":none", // video:audio — "none" disables audio capture
		}
	default:
		// Linux: V4L2 device path (e.g., /dev/video0)
		inputArgs = []string{
			"-v", "warning",
			"-f", "v4l2",
			"-framerate", "30",
			"-video_size", fmt.Sprintf("%dx%d", captureWidth, captureHeight),
			"-i", source,
		}
	}

	return append(inputArgs,
		"-pix_fmt", "yuv420p", // force 4:2:0 — macOS cameras output 4:2:2 which baseline H264 rejects
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-profile:v", "baseline",
		"-g", strconv.Itoa(captureFPS*2), // keyframe every 2 seconds
		"-s", fmt.Sprintf("%dx%d", captureWidth, captureHeight),
		"-r", strconv.Itoa(captureFPS),
		"-an", // no audio
		"-f", "rtsp",
		"-rtsp_transport", "tcp",
		rtspOutput,
	)
}

// Close stops the source publisher and its ffmpeg subprocess.
func (p *SourcePublisher) Close() {
	if p.cancel != nil {
		p.cancel()
	}
}
