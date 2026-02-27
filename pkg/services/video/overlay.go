//go:build detection

package video

import (
	"fmt"
	"sync"
	"time"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"gocv.io/x/gocv"
)

// OverlayWriter publishes annotated H264 frames to an RTSP server.
// It automatically reconnects if the connection drops.
type OverlayWriter struct {
	mu      sync.Mutex
	client  *gortsplib.Client
	desc    *description.Session
	forma   *format.H264
	rtpEnc  *rtph264.Encoder
	h264enc *H264Encoder
	start   time.Time
	closed  bool

	// reconnect state
	rtspURL  string
	failures int
}

// NewOverlayWriter creates a new overlay publisher that pushes H264 to
// rtsp://{rtspHost}:{rtspPort}/{entityID}/pulsar.
func NewOverlayWriter(entityID, rtspHost string, rtspPort int) (*OverlayWriter, error) {
	url := fmt.Sprintf("rtsp://%s:%d/%s/pulsar", rtspHost, rtspPort, entityID)

	h264enc, err := NewH264Encoder(captureWidth, captureHeight, captureFPS)
	if err != nil {
		return nil, fmt.Errorf("h264 encoder: %w", err)
	}

	w := &OverlayWriter{
		rtspURL: url,
		h264enc: h264enc,
		forma: &format.H264{
			PayloadTyp:        96,
			PacketizationMode: 1,
		},
	}
	w.desc = &description.Session{
		Medias: []*description.Media{{
			Type:    description.MediaTypeVideo,
			Formats: []format.Format{w.forma},
		}},
	}

	// Set initial SPS/PPS if available from encoder headers
	if sps := h264enc.SPS(); sps != nil {
		w.forma.SPS = sps
		w.forma.PPS = h264enc.PPS()
	}

	if err := w.connect(); err != nil {
		h264enc.Close()
		return nil, err
	}

	logger.Infof("[overlay] publishing H264 to %s", url)
	return w, nil
}

// connect establishes (or re-establishes) the RTSP recording session.
func (w *OverlayWriter) connect() error {
	// Force TCP interleaved transport so RTP packets flow over the same
	// TCP connection as RTSP control. This keeps the connection alive —
	// with UDP, the TCP control channel gets no traffic and MediaMTX's
	// readTimeout (default 10s) kills the session.
	proto := gortsplib.ProtocolTCP
	c := &gortsplib.Client{
		Protocol:     &proto,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	if err := c.StartRecording(w.rtspURL, w.desc); err != nil {
		return fmt.Errorf("start overlay recording at %s: %w", w.rtspURL, err)
	}

	rtpEnc, err := w.forma.CreateEncoder()
	if err != nil {
		c.Close()
		return fmt.Errorf("create H264 RTP encoder: %w", err)
	}
	rtpEnc.PayloadMaxSize = 1200 // WebRTC-safe; default 1450 triggers MediaMTX remuxing

	w.client = c
	w.rtpEnc = rtpEnc
	w.start = time.Now()
	w.failures = 0
	return nil
}

// reconnect closes the old client and re-establishes the session.
func (w *OverlayWriter) reconnect() bool {
	if w.client != nil {
		w.client.Close()
		w.client = nil
	}
	w.failures++
	backoff := time.Duration(w.failures) * time.Second
	if backoff > 10*time.Second {
		backoff = 10 * time.Second
	}
	time.Sleep(backoff)

	if err := w.connect(); err != nil {
		if w.failures <= 3 || w.failures%10 == 0 {
			logger.Errorf("[overlay] reconnect to %s failed (%d): %v", w.rtspURL, w.failures, err)
		}
		return false
	}
	logger.Infof("[overlay] reconnected to %s after %d attempts", w.rtspURL, w.failures)
	return true
}

// WriteFrame encodes a gocv.Mat as H264 and pushes it via RTSP.
func (w *OverlayWriter) WriteFrame(frame gocv.Mat) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return fmt.Errorf("overlay writer closed")
	}

	img, err := frame.ToImage()
	if err != nil {
		return fmt.Errorf("mat to image: %w", err)
	}

	au, err := w.h264enc.Encode(img)
	if err != nil {
		return fmt.Errorf("h264 encode: %w", err)
	}
	if au == nil {
		return nil // encoder buffering
	}

	// Update SPS/PPS on the format for live SDP updates
	if sps := w.h264enc.SPS(); sps != nil {
		w.forma.SafeSetParams(sps, w.h264enc.PPS())
	}

	pkts, err := w.rtpEnc.Encode(au)
	if err != nil {
		return fmt.Errorf("rtp encode: %w", err)
	}

	elapsed := time.Since(w.start)
	ts := uint32(int64(elapsed) * int64(w.forma.ClockRate()) / int64(time.Second))

	for _, pkt := range pkts {
		pkt.Timestamp = ts
		if err := w.client.WritePacketRTP(w.desc.Medias[0], pkt); err != nil {
			if w.reconnect() {
				return nil
			}
			return fmt.Errorf("write rtp (disconnected): %w", err)
		}
	}
	return nil
}

// Close shuts down the RTSP connection and H264 encoder.
func (w *OverlayWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		if w.client != nil {
			w.client.Close()
		}
		if w.h264enc != nil {
			w.h264enc.Close()
		}
		logger.Info("[overlay] closed")
	}
}
