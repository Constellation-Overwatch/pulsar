package video

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
)

// RTSPServer abstracts MediaMTX (external) vs embedded gortsplib server.
type RTSPServer struct {
	mode     string // "mediamtx" or "embedded"
	embedded *gortsplib.Server
	handler  *embeddedHandler
	rtspHost string
	rtspPort int
}

// NewRTSPServer probes for MediaMTX on the RTSP port (with retries).
// If found, uses it. Otherwise starts an embedded gortsplib RTSP server.
func NewRTSPServer(rtspHost string, rtspPort int) *RTSPServer {
	srv := &RTSPServer{
		rtspHost: rtspHost,
		rtspPort: rtspPort,
	}

	rtspAddr := net.JoinHostPort(rtspHost, strconv.Itoa(rtspPort))

	// Probe MediaMTX by TCP-dialing the RTSP port
	for attempt := 1; attempt <= 5; attempt++ {
		conn, err := net.DialTimeout("tcp", rtspAddr, 2*time.Second)
		if err == nil {
			conn.Close()
			logger.Infof("[video] mediamtx detected at %s, using external RTSP server", rtspAddr)
			srv.mode = "mediamtx"
			return srv
		}
		if attempt < 5 {
			logger.Warnf("[video] mediamtx not ready at %s, retrying (%d/5)...", rtspAddr, attempt)
			time.Sleep(2 * time.Second)
		}
	}

	// Fallback: embedded gortsplib RTSP server
	logger.Infof("[video] mediamtx not found at %s, starting embedded RTSP server on :%d", rtspAddr, rtspPort)
	h := newEmbeddedHandler()
	s := &gortsplib.Server{
		Handler:     h,
		RTSPAddress: fmt.Sprintf(":%d", rtspPort),
	}
	if err := s.Start(); err != nil {
		logger.Errorf("[video] embedded RTSP server failed to start: %v", err)
		logger.Warn("[video] running without RTSP server (video unavailable)")
		srv.mode = "none"
		return srv
	}
	h.server = s
	srv.mode = "embedded"
	srv.embedded = s
	srv.handler = h
	logger.Infof("[video] embedded RTSP server started on :%d", rtspPort)
	return srv
}

// Mode returns "mediamtx", "embedded", or "none".
func (s *RTSPServer) Mode() string { return s.mode }

// Close shuts down the embedded server if running.
func (s *RTSPServer) Close() {
	if s.embedded != nil {
		s.embedded.Close()
		logger.Info("[video] embedded RTSP server stopped")
	}
}

// ── Embedded gortsplib RTSP Server ──────────────────────────────────────────

type streamEntry struct {
	desc   *description.Session
	stream *gortsplib.ServerStream
}

type embeddedHandler struct {
	server  *gortsplib.Server
	mu      sync.RWMutex
	streams map[string]*streamEntry
}

func newEmbeddedHandler() *embeddedHandler {
	return &embeddedHandler{
		streams: make(map[string]*streamEntry),
	}
}

func (h *embeddedHandler) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	h.mu.RLock()
	se, ok := h.streams[ctx.Path]
	h.mu.RUnlock()

	if !ok {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, se.stream, nil
}

func (h *embeddedHandler) OnAnnounce(ctx *gortsplib.ServerHandlerOnAnnounceCtx) (*base.Response, error) {
	// Store description only — stream is created later in OnRecord.
	// OnSetup must return nil stream for publishers (gortsplib v5 requirement).
	h.mu.Lock()
	if prev, ok := h.streams[ctx.Path]; ok && prev.stream != nil {
		prev.stream.Close()
	}
	h.streams[ctx.Path] = &streamEntry{desc: ctx.Description, stream: nil}
	h.mu.Unlock()

	logger.Infof("[video/embedded] publisher announced at %s", ctx.Path)
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (h *embeddedHandler) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	h.mu.RLock()
	se, ok := h.streams[ctx.Path]
	h.mu.RUnlock()

	if !ok {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, se.stream, nil
}

func (h *embeddedHandler) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	logger.Infof("[video/embedded] reader playing /%s", ctx.Path)
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (h *embeddedHandler) OnRecord(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	// Create the ServerStream now that the publisher is ready to record.
	// This is where gortsplib v5 expects stream creation for publishers.
	stream := &gortsplib.ServerStream{
		Server: h.server,
		Desc:   ctx.Session.AnnouncedDescription(),
	}
	if err := stream.Initialize(); err != nil {
		logger.Errorf("[video/embedded] failed to init stream for %s: %v", ctx.Path, err)
		return &base.Response{StatusCode: base.StatusInternalServerError}, nil
	}

	h.mu.Lock()
	if prev, ok := h.streams[ctx.Path]; ok && prev.stream != nil {
		prev.stream.Close()
	}
	h.streams[ctx.Path] = &streamEntry{desc: ctx.Session.AnnouncedDescription(), stream: stream}
	h.mu.Unlock()

	// Forward RTP packets from publisher session to the stream (fan-out to readers)
	ctx.Session.OnPacketRTPAny(func(medi *description.Media, forma format.Format, pkt *rtp.Packet) {
		stream.WritePacketRTP(medi, pkt)
	})

	logger.Infof("[video/embedded] publisher recording at %s", ctx.Path)
	return &base.Response{StatusCode: base.StatusOK}, nil
}
