# Pulsar v0.0.1 — Initial Release

The first public release of Pulsar, the edge sync agent for Constellation Overwatch.

## Highlights

Pulsar runs as a single Go binary on ground control stations, connecting fleet assets to Overwatch with zero manual API calls. Define your fleet in YAML, point it at Overwatch, and everything else is automatic.

## Features

### Fleet Registration & Reconciliation
- **Guided first-time setup** — interactive terminal wizard generates `config/fleet.yaml` on first boot
- **Declarative fleet config** — define entities (drones, sensors, vehicles) in YAML
- **Idempotent registration** — entity UUIDs tracked in `config/c4.json` across restarts, no duplicates
- **Drift detection** — detects name, priority, and status changes; updates Overwatch automatically
- **Stale entity cleanup** — removes entities that no longer exist in config or on Overwatch
- **Live sync loop** — watches `config/fleet.yaml` every 30s, re-registers and restarts services on change

### MAVLink Telemetry
- **Per-entity UDP listeners** — 1:1 MAVLink relay with auto-assigned sequential ports from `MAVLINK_BASE_PORT`
- **Explicit port override** — reserve specific ports per entity with `mavlink: {port: N}`
- **NATS JetStream publishing** — telemetry envelopes on `constellation.telemetry.{entity_id}.{msg_type}`
- **KV state aggregation** — per-entity device state merged by message type into `CONSTELLATION_GLOBAL_STATE`
- **Supported message types** — Heartbeat, GlobalPositionInt, Attitude, VFR_HUD, SystemStatus

### Video Pipeline
- **RTSP video bridge** — per-entity video relay with MediaMTX auto-detection
- **Local device capture** — camera capture via OpenCV (build-tag gated)
- **Embedded RTSP fallback** — gortsplib server when MediaMTX is unavailable
- **Dual host configuration** — separate `RTSP_HOST` (local) and `ADVERTISE_HOST` (external) for multi-NIC setups
- **WebRTC/HLS support** — browser-ready streams via MediaMTX sidecar

### On-Device Detection (optional, `-tags detection`)
- **YOLO26 ONNX inference** — e2e NMS-free output format (Jan 2026 models)
- **Legacy YOLOv8/v11 support** — transposed output with manual NMS via `MODEL_FORMAT=legacy`
- **Bounding-box overlay** — detection results rendered on video stream at `/pulsar` suffix
- **Frame-skip optimization** — YOLO runs every 5th frame, cached detections drawn on all frames (15fps smooth)
- **Build-tag isolation** — `//go:build detection` gates all CGO/CV code; no-op stubs for clean default builds

### Deployment
- **Single static binary** — `CGO_ENABLED=0` for standard builds (~15-25MB)
- **Multi-stage Docker image** — Alpine-based, includes `ca-certificates`
- **Docker Compose stack** — Pulsar + MediaMTX + TAK Server
- **Cross-platform** — Linux and macOS (amd64, arm64)
- **Task runner** — 20+ commands for build, dev, test, and deployment workflows

## Configuration

| Variable | Required | Description |
| --- | --- | --- |
| `C4_API_KEY` | Yes | Overwatch API bearer token |
| `C4_BASE_URL` | Yes | Overwatch API URL |
| `C4_NATS_KEY` | Yes | NATS nkey seed for JetStream auth |
| `C4_NATS_URL` | No | NATS server URL (default: `nats://localhost:4222`) |
| `MAVLINK_BASE_PORT` | No | Starting MAVLink port (default: `14550`) |
| `RTSP_HOST` | No | Local RTSP hostname (default: `localhost`) |
| `ADVERTISE_HOST` | No | External hostname for Overwatch (default: auto-discovered) |

See [README.md](README.md) for full configuration reference.

## Known Limitations

- **Test coverage** — unit tests cover registration logic only (`registry_test.go`); relay, publisher, and video packages need test suites
- **CGO required for detection** — OpenCV + ONNX Runtime needed for `-tags detection` builds; CGO-free migration planned (see `docs/CGO_FREE_ARCHITECTURE.md`)
- **Single RTSP port** — all entities share the same RTSP port with path-based routing; per-entity port assignment not yet supported
- **No TLS** — NATS and Overwatch API connections are unencrypted by default

## Upgrade Notes

This is the initial release. No upgrade path from prior versions.

## Dependencies

- Go 1.25
- gomavlib/v3 (MAVLink V2, common dialect)
- gortsplib/v5 (embedded RTSP server)
- nats.go v1.49 (JetStream + KV)
- gocv v0.43 + onnxruntime_go v1.26 (detection, optional)
- x264-go (local fork with IDR keyframe fix for WebRTC)
