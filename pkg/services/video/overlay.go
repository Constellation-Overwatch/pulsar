package video

import (
	"context"
	"fmt"
	"image"
	"io"
	"os/exec"
	"strconv"
	"sync"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"
)

// OverlayWriter publishes annotated H264 frames to an RTSP path via ffmpeg.
// Raw RGB24 frames are piped to ffmpeg's stdin; ffmpeg encodes and publishes
// directly to the RTSP server (MediaMTX or embedded). This eliminates the
// pipe deadlock, SPS/PPS initialization, and RTP packetization issues from
// the previous gortsplib-based approach.
type OverlayWriter struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	closed   bool
	width    int
	height   int
	ctx      context.Context
	rtspURL  string
	failures int
}

// NewOverlayWriter creates a new overlay publisher that uses ffmpeg to push
// H264 to rtsp://{rtspHost}:{rtspPort}/{entityID}/pulsar.
func NewOverlayWriter(ctx context.Context, entityID, rtspHost string, rtspPort int) (*OverlayWriter, error) {
	url := fmt.Sprintf("rtsp://%s:%d/%s/pulsar", rtspHost, rtspPort, entityID)

	w := &OverlayWriter{
		ctx:     ctx,
		rtspURL: url,
		width:   captureWidth,
		height:  captureHeight,
	}

	if err := w.startFFmpeg(); err != nil {
		return nil, err
	}

	logger.Infof("[overlay] publishing H264 to %s via ffmpeg", url)
	return w, nil
}

// startFFmpeg spawns the ffmpeg encoding/publishing subprocess.
func (w *OverlayWriter) startFFmpeg() error {
	args := buildOverlayArgs(w.width, w.height, w.rtspURL)
	cmd := exec.CommandContext(w.ctx, "ffmpeg", args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("ffmpeg stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return fmt.Errorf("ffmpeg start: %w", err)
	}

	w.cmd = cmd
	w.stdin = stdin
	w.failures = 0
	return nil
}

// buildOverlayArgs constructs ffmpeg arguments for RGB24 stdin → H264 RTSP output.
func buildOverlayArgs(width, height int, rtspURL string) []string {
	return []string{
		"-v", "warning",
		"-f", "rawvideo",
		"-pix_fmt", "rgb24",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", strconv.Itoa(captureFPS),
		"-i", "pipe:0",
		"-pix_fmt", "yuv420p",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-profile:v", "baseline",
		"-g", strconv.Itoa(captureFPS * 2), // keyframe every 2 seconds
		"-f", "rtsp",
		"-rtsp_transport", "tcp",
		rtspURL,
	}
}

// reconnect closes the old ffmpeg process and starts a new one.
func (w *OverlayWriter) reconnect() bool {
	if w.stdin != nil {
		w.stdin.Close()
	}
	if w.cmd != nil && w.cmd.Process != nil {
		w.cmd.Process.Kill()
		w.cmd.Wait()
	}

	w.failures++
	if err := w.startFFmpeg(); err != nil {
		if w.failures <= 3 || w.failures%10 == 0 {
			logger.Warnf("[overlay] reconnect to %s failed (%d): %v", w.rtspURL, w.failures, err)
		}
		return false
	}

	logger.Infof("[overlay] reconnected to %s after %d attempts", w.rtspURL, w.failures)
	return true
}

// WriteFrame converts an image.Image to RGB24 and pipes it to ffmpeg.
func (w *OverlayWriter) WriteFrame(img image.Image) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return fmt.Errorf("overlay writer closed")
	}

	frame := imageToRGB24(img, w.width, w.height)
	if _, err := w.stdin.Write(frame); err != nil {
		// ffmpeg may have crashed; try to restart
		if w.reconnect() {
			if _, err := w.stdin.Write(frame); err != nil {
				return fmt.Errorf("write frame after reconnect: %w", err)
			}
			return nil
		}
		return fmt.Errorf("write frame (disconnected): %w", err)
	}
	return nil
}

// Close shuts down the ffmpeg process.
func (w *OverlayWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		if w.stdin != nil {
			w.stdin.Close()
		}
		if w.cmd != nil && w.cmd.Process != nil {
			w.cmd.Process.Kill()
			w.cmd.Wait()
		}
		logger.Info("[overlay] closed")
	}
}

// imageToRGB24 converts an image.Image to raw RGB24 bytes at the given dimensions.
func imageToRGB24(img image.Image, w, h int) []byte {
	buf := make([]byte, w*h*3)
	bounds := img.Bounds()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			srcX := bounds.Min.X + x
			srcY := bounds.Min.Y + y
			if srcX < bounds.Max.X && srcY < bounds.Max.Y {
				r, g, b, _ := img.At(srcX, srcY).RGBA()
				off := (y*w + x) * 3
				buf[off] = byte(r >> 8)
				buf[off+1] = byte(g >> 8)
				buf[off+2] = byte(b >> 8)
			}
		}
	}
	return buf
}
