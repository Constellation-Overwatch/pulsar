module github.com/Constellation-Overwatch/pulsar

go 1.25.0

require (
	github.com/bluenviron/gomavlib/v3 v3.3.0
	github.com/bluenviron/gortsplib/v5 v5.3.2
	github.com/bluenviron/mediacommon/v2 v2.8.0
	github.com/gen2brain/x264-go v0.3.1
	github.com/google/uuid v1.6.0
	github.com/joho/godotenv v1.5.1
	github.com/nats-io/nats.go v1.49.0
	github.com/nats-io/nkeys v0.4.15
	github.com/pion/rtp v1.10.1
	github.com/yalue/onnxruntime_go v1.26.0
	go.uber.org/zap v1.27.1
	gocv.io/x/gocv v0.43.0
	gopkg.in/yaml.v3 v3.0.1
)

// Local patch: disable BIntraRefresh for WebRTC-compatible IDR keyframes
replace github.com/gen2brain/x264-go v0.3.1 => ./internal/x264-go

require (
	github.com/creack/goselect v0.1.2 // indirect
	github.com/gen2brain/x264-go/x264c v0.0.0-20241022182000-732e1bdb7da2 // indirect
	github.com/gen2brain/x264-go/yuv v0.0.0-20221204084822-82ee2951dea2 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/klauspost/compress v1.18.2 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.16 // indirect
	github.com/pion/sdp/v3 v3.0.18 // indirect
	github.com/pion/srtp/v3 v3.0.10 // indirect
	github.com/pion/transport/v2 v2.2.10 // indirect
	github.com/pion/transport/v4 v4.0.1 // indirect
	go.bug.st/serial v1.6.4 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
)
