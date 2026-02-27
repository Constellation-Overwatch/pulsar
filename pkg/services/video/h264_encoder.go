//go:build detection

package video

import (
	"bytes"
	"fmt"
	"image"
	"sync"

	x264 "github.com/gen2brain/x264-go"
	mch264 "github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
)

// H264Encoder wraps x264-go to produce NAL unit access units
// compatible with gortsplib's rtph264.Encoder.Encode(au [][]byte).
type H264Encoder struct {
	mu  sync.Mutex
	enc *x264.Encoder
	buf *bytes.Buffer
	sps []byte
	pps []byte
}

// NewH264Encoder creates an x264 encoder configured for low-latency WebRTC streaming.
func NewH264Encoder(width, height, fps int) (*H264Encoder, error) {
	buf := &bytes.Buffer{}
	opts := &x264.Options{
		Width:     width,
		Height:    height,
		FrameRate: fps,
		Tune:      "zerolatency",
		Preset:    "ultrafast",
		Profile:   "baseline",
		LogLevel:  x264.LogWarning,
	}

	enc, err := x264.NewEncoder(buf, opts)
	if err != nil {
		return nil, fmt.Errorf("x264 init: %w", err)
	}

	h := &H264Encoder{enc: enc, buf: buf}

	// x264 writes SPS/PPS headers to buf during NewEncoder.
	// Parse them out now.
	if buf.Len() > 0 {
		h.parseParams(buf.Bytes())
		buf.Reset()
	}

	return h, nil
}

// Encode converts a Go image to an H264 access unit (slice of NAL units).
// Returns nil, nil when x264 is buffering and has no output yet.
func (h *H264Encoder) Encode(img image.Image) ([][]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.buf.Reset()

	if err := h.enc.Encode(img); err != nil {
		return nil, fmt.Errorf("x264 encode: %w", err)
	}

	data := h.buf.Bytes()
	if len(data) == 0 {
		return nil, nil
	}

	var au mch264.AnnexB
	if err := au.Unmarshal(data); err != nil {
		return nil, fmt.Errorf("annex-b unmarshal: %w", err)
	}

	h.parseParams(data)

	return [][]byte(au), nil
}

// parseParams extracts SPS and PPS from Annex-B data.
func (h *H264Encoder) parseParams(data []byte) {
	var au mch264.AnnexB
	if err := au.Unmarshal(data); err != nil {
		return
	}
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		switch mch264.NALUType(nalu[0] & 0x1F) {
		case mch264.NALUTypeSPS:
			h.sps = make([]byte, len(nalu))
			copy(h.sps, nalu)
		case mch264.NALUTypePPS:
			h.pps = make([]byte, len(nalu))
			copy(h.pps, nalu)
		}
	}
}

// SPS returns the cached Sequence Parameter Set.
func (h *H264Encoder) SPS() []byte { return h.sps }

// PPS returns the cached Picture Parameter Set.
func (h *H264Encoder) PPS() []byte { return h.pps }

// Close releases encoder resources.
func (h *H264Encoder) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = h.enc.Flush()
	return h.enc.Close()
}
