package detector

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

// FrameReader captures video frames from an RTSP or device source using an
// ffmpeg subprocess. Frames are delivered as raw RGB24 via stdout pipe and
// converted to *image.NRGBA for downstream processing.
type FrameReader struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	width  int
	height int
	buf    []byte // reusable read buffer (width * height * 3 bytes)
}

// NewFrameReader spawns an ffmpeg process that decodes the given source and
// outputs raw RGB24 frames at the requested resolution and frame rate.
// The source can be an RTSP URL or a local device path (/dev/videoN on Linux,
// avfoundation index on macOS).
func NewFrameReader(ctx context.Context, source string, w, h, fps int) (*FrameReader, error) {
	args := buildFFmpegArgs(source, w, h, fps)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stderr = nil // discard ffmpeg log output

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}

	return &FrameReader{
		cmd:    cmd,
		stdout: stdout,
		width:  w,
		height: h,
		buf:    make([]byte, w*h*3),
	}, nil
}

// buildFFmpegArgs constructs the ffmpeg argument list based on source type.
func buildFFmpegArgs(source string, w, h, fps int) []string {
	size := fmt.Sprintf("%dx%d", w, h)
	rate := fmt.Sprintf("%d", fps)

	if strings.HasPrefix(source, "rtsp://") {
		return []string{
			"-rtsp_transport", "tcp",
			"-i", source,
			"-f", "rawvideo",
			"-pix_fmt", "rgb24",
			"-s", size,
			"-r", rate,
			"-an",           // no audio
			"-v", "warning", // reduce log noise
			"pipe:1",
		}
	}

	// Device source: use platform-specific input format
	switch runtime.GOOS {
	case "darwin":
		// macOS: avfoundation — request target resolution natively,
		// capture at 30fps. Output as raw RGB24 at target rate.
		return []string{
			"-f", "avfoundation",
			"-video_size", size,
			"-framerate", "30",
			"-i", source + ":none", // video:audio — "none" disables audio capture
			"-f", "rawvideo",
			"-pix_fmt", "rgb24",
			"-s", size,
			"-r", rate,
			"-an",
			"-v", "warning",
			"pipe:1",
		}
	default:
		// Linux: V4L2 device path (e.g., /dev/video0)
		return []string{
			"-f", "v4l2",
			"-framerate", rate,
			"-video_size", size,
			"-i", source,
			"-f", "rawvideo",
			"-pix_fmt", "rgb24",
			"-s", size,
			"-r", rate,
			"-an",
			"-v", "warning",
			"pipe:1",
		}
	}
}

// Read returns the next decoded video frame as an *image.NRGBA.
// Returns io.EOF when the ffmpeg process exits or the source ends.
func (f *FrameReader) Read() (image.Image, error) {
	_, err := io.ReadFull(f.stdout, f.buf)
	if err != nil {
		return nil, err
	}

	img := image.NewNRGBA(image.Rect(0, 0, f.width, f.height))
	for y := 0; y < f.height; y++ {
		for x := 0; x < f.width; x++ {
			off := (y*f.width + x) * 3
			img.SetNRGBA(x, y, color.NRGBA{
				R: f.buf[off],
				G: f.buf[off+1],
				B: f.buf[off+2],
				A: 255,
			})
		}
	}
	return img, nil
}

// Close terminates the ffmpeg process and releases resources.
func (f *FrameReader) Close() error {
	if f.stdout != nil {
		f.stdout.Close()
	}
	if f.cmd != nil && f.cmd.Process != nil {
		f.cmd.Process.Kill()
		f.cmd.Wait()
	}
	return nil
}
