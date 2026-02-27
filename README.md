<p align="center">
  <img src="public/favicon.svg" alt="Pulsar" width="128" height="128">
</p>

<h1 align="center">Pulsar</h1>

<p align="center">
  Edge Sync Agent for Constellation Overwatch — MAVLink Relay, Video Bridge, and Fleet Registration for GCS-in-a-Box
</p>

<p align="center">
  <a title="Go Report Card" target="_blank" href="https://goreportcard.com/report/github.com/Constellation-Overwatch/pulsar"><img src="https://goreportcard.com/badge/github.com/Constellation-Overwatch/pulsar?style=flat-square"></a>
  <a title="Go Version" target="_blank" href="https://go.dev/"><img src="https://img.shields.io/github/go-mod/go-version/Constellation-Overwatch/pulsar?style=flat-square"></a>
  <a title="License" target="_blank" href="https://github.com/Constellation-Overwatch/pulsar/blob/main/LICENSE"><img src="http://img.shields.io/badge/license-MIT-orange.svg?style=flat-square"></a>
</p>

---

## About

Pulsar is the edge sync agent that connects ground control stations to [Constellation Overwatch](https://github.com/Constellation-Overwatch/constellation-overwatch). It runs as a single Go binary on a GCS device, auto-registers fleet entities with Overwatch, relays MAVLink telemetry over NATS JetStream, bridges RTSP video streams, and runs optional on-device YOLO detection.

> **Warning:** This software is under active development. While functional, it may contain bugs and undergo breaking changes.

## Features

* **Guided First-Time Setup** — interactive terminal wizard generates `config/fleet.yaml` when no config exists
* **Declarative Fleet Config** — define entities in `config/fleet.yaml`, Pulsar handles registration and reconciliation
* **Idempotent Registration** — entity UUIDs tracked in `config/c4.json` across restarts, no duplicates
* **MAVLink Relay** — 1:1 UDP listeners per entity with auto-assigned sequential ports
* **NATS JetStream Publishing** — telemetry envelopes with headers on `constellation.telemetry.{entity_id}.{msg_type}`
* **KV State Aggregation** — per-entity device state merged by message type into `CONSTELLATION_GLOBAL_STATE`
* **RTSP Video Bridge** — per-entity video relay with optional device capture
* **On-Device Detection** — ONNX/YOLO inference with bounding-box overlay (build-tag gated)
* **Live Sync Loop** — watches `config/fleet.yaml` for changes, re-registers and restarts services without downtime
* **Docker Ready** — multi-stage Alpine image, single binary

## Architecture

### Boot Flow

```text
.env (credentials)       config/fleet.yaml (desired)     config/c4.json (actual)
       |                           |                                |
       v                           v                                v
   Load env vars          Load or guided setup              Load previous state
       |                           |                                |
       +---------------------------+--------------------------------+
                                   |
                                   v
                          Overwatch Registration
                          (reconcile by entity_id)
                                   |
                                   v
                          Write config/c4.json
                          (resolved UUIDs + ports)
                                   |
                    +--------------+--------------+
                    |              |              |
                    v              v              v
              MAVLink Relay   Video Bridge   Detector
              (per-entity     (per-entity    (ONNX/YOLO
               UDP listeners)  RTSP bridge)   inference)
                    |
                    v
              NATS JetStream
              (publish telemetry + update KV)
```

### Data Flow

```mermaid
sequenceDiagram
    participant D as Drone (MAVLink)
    participant R as Pulsar Relay
    participant N as NATS JetStream
    participant KV as KV Store<br/>(CONSTELLATION_GLOBAL_STATE)
    participant OW as Overwatch API

    Note over R,OW: Boot: Registration
    R->>OW: Register org + entities (config/fleet.yaml)
    OW-->>R: Entity UUIDs
    R->>R: Write config/c4.json + resolve MAVLink ports

    Note over D,KV: Runtime: Telemetry Flow
    D->>R: MAVLink UDP frames (auto-assigned port)
    R->>R: Parse frame, build TelemetryEnvelope
    R->>N: Publish to constellation.telemetry.{entity_id}.{msg_type}
    R->>KV: Merge device state by message type
    KV-->>OW: Overwatch reads latest state
```

### fleet.yaml vs c4.json

| | `fleet.yaml` | `c4.json` |
| --- | --- | --- |
| **Role** | Desired state | Actual state |
| **Author** | Human | Machine |
| **Git** | Committed | Gitignored |
| **Contains** | Names, types, `mavlink: true` | UUIDs, resolved ports, RTSP URLs |

`fleet.yaml` is what you **want**. `c4.json` is what **is**. Pulsar reconciles the two on every boot and sync cycle, using entity names to track UUIDs across restarts without duplicating registrations.

## Getting Started

<details>
<summary>Prerequisites</summary>
<br>

* Go 1.25 or higher
* A running [Constellation Overwatch](https://github.com/Constellation-Overwatch/constellation-overwatch) instance
* [Task](https://taskfile.dev/) runner (optional, recommended)

</details>

<details>
<summary>Quick Start</summary>
<br>

```bash
# Clone
git clone https://github.com/Constellation-Overwatch/pulsar.git
cd pulsar

# Copy env and configure credentials
cp .env.example .env
# Edit .env with your Overwatch API key and NATS key

# Or copy the example fleet config
cp config/fleet.example.yaml config/fleet.yaml

# Run (guided setup will create config/fleet.yaml on first boot if missing)
task dev
```

On first run with no `config/fleet.yaml`, Pulsar walks you through setup:

```text
=== Pulsar First-Time Setup ===

  Organization name [GCS Alpha Station]:
  Organization type [civilian]:
  How many entities to register? [1]: 3

--- Entity 1 of 3 ---
  Entity name [Entity 1]: Primary UAV
  Entity type [uav]: uav
  Priority [normal]: high
  Enable MAVLink telemetry? (y/n, ports auto-assigned from 14550) [y]: y
  Enable video stream? (y/n) [n]: y
    Video source types:
      rtsp   - Network RTSP source (camera, MediaMTX, etc.)
      device - Local capture device (/dev/video0)
  Video source type [rtsp]: rtsp
  RTSP source URL (e.g., rtsp://user:pass@192.168.1.50:554/stream): rtsp://admin:secret@10.0.0.50:554/cam1

--- Entity 2 of 3 ---
  Entity name [Entity 2]: Secondary UAV
  Entity type [uav]: uav
  Priority [normal]: normal
  Enable MAVLink telemetry? (y/n, ports auto-assigned from 14550) [y]: y
  Enable video stream? (y/n) [n]: n

--- Entity 3 of 3 ---
  Entity name [Entity 3]: Ground Camera
  Entity type [uav]: isr_sensor
  Priority [normal]: normal
  Enable MAVLink telemetry? (y/n, ports auto-assigned from 14550) [y]: n
  Enable video stream? (y/n) [n]: y
    Video source types:
      rtsp   - Network RTSP source (camera, MediaMTX, etc.)
      device - Local capture device (/dev/video0)
  Video source type [rtsp]: device
  Device path [/dev/video0]: /dev/video0

=== Fleet Summary ===
  Organization: GCS Alpha Station (civilian)
  Entities:     3 total, 2 with MAVLink, 2 with video
    - Primary UAV [uav] -> mavlink video(rtsp:rtsp://admin:***@10.0.0.50:554/cam1)
    - Secondary UAV [uav] -> mavlink
    - Ground Camera [isr_sensor] -> video(device:/dev/video0)
```

This generates `config/fleet.yaml` and registers with Overwatch. MAVLink ports are auto-assigned:

```text
[pulsar] mavlink: Primary UAV -> UDP :14550
[pulsar] mavlink: Secondary UAV -> UDP :14551
```

</details>

### Configuration

**Environment variables** (`.env`):

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `C4_API_KEY` | Yes | — | Overwatch API bearer token |
| `C4_BASE_URL` | Yes | — | Overwatch API URL (e.g. `http://localhost:8080`) |
| `C4_NATS_KEY` | Yes | — | NATS nkey seed for JetStream auth |
| `C4_NATS_URL` | No | `nats://localhost:4222` | NATS server URL |
| `MAVLINK_BASE_PORT` | No | `14550` | Starting port for auto-assigned MAVLink listeners |
| `FLEET_CONFIG` | No | `config/fleet.yaml` | Path to fleet config file |
| `C4_STATE_FILE` | No | `config/c4.json` | Path to state file |
| `RTSP_HOST` | No | `localhost` | Hostname for local RTSP URL construction (used by bridge/detector) |
| `ADVERTISE_HOST` | No | Auto-discovered (first non-loopback IPv4) | Hostname/IP published to Overwatch for external consumers |
| `MEDIAMTX_API_URL` | No | `http://localhost:9997` | MediaMTX API URL for RTSP server auto-detection |
| `MODEL_PATH` | No | `models/yoloe-11s-seg.onnx` | ONNX model path for detection |

**Fleet config** (`config/fleet.yaml`):

```yaml
organization:
  name: "GCS Alpha Station"
  type: "civilian"
  description: "Rapid response ground control station"

entities:
  - name: "Primary UAV"
    type: "uav"
    priority: "high"
    status: "active"
    mavlink: true                # auto-assign port from MAVLINK_BASE_PORT
    video_config:
      protocol: "rtsp"
      port: 8554
      source: "rtsp://admin:secret@10.0.0.50:554/cam1"  # network RTSP with auth

  - name: "Secondary UAV"
    type: "uav"
    priority: "normal"
    status: "active"
    mavlink: true                # gets next sequential port

  - name: "Fixed Overwatch"
    type: "uav"
    priority: "low"
    status: "active"
    mavlink:
      port: 14560              # explicit port override

  - name: "Ground Camera"
    type: "isr_sensor"
    priority: "normal"
    status: "active"
    video_config:
      protocol: "rtsp"
      port: 8554
      device: "/dev/video0"    # local capture device
    # no mavlink key = no listener
```

**Entity types:** `uav`, `fixed_wing`, `vtol`, `helicopter`, `airship`, `ground_vehicle`, `boat`, `isr_sensor`, `camera`, `gcs`

**Organization types:** `military`, `civilian`, `commercial`, `ngo`

### MAVLink Port Assignment

Ports are auto-assigned sequentially from `MAVLINK_BASE_PORT` (default `14550`):

| Entity | Config | Resolved Port |
| --- | --- | --- |
| Primary UAV | `mavlink: true` | `:14550` |
| Secondary UAV | `mavlink: true` | `:14551` |
| Fixed Overwatch | `mavlink: {port: 14560}` | `:14560` |
| Ground Camera | *(no mavlink key)* | *(no listener)* |

Explicit ports are reserved first, then auto-assigned ports fill in from the base, skipping conflicts.

### Video Sources

Each entity can optionally have a `video_config` with one of two source types:

| Source Type | Config Key | Example | Description |
| --- | --- | --- | --- |
| Network RTSP | `source` | `rtsp://admin:pass@10.0.0.50:554/cam1` | Proxied through MediaMTX or read directly by detector |
| Local Device | `device` | `/dev/video0` | Captured via OpenCV, published as MJPEG to RTSP server |
| *(none)* | *(omit video_config)* | — | No video for this entity |

RTSP credentials are embedded in the source URL per RFC 2396 (`rtsp://user:pass@host:port/path`). Pulsar auto-detects whether MediaMTX is running (probes its API at `MEDIAMTX_API_URL`); if found, it configures on-demand source proxying. Otherwise it starts an embedded gortsplib RTSP server as fallback.

### Video Host Configuration

Pulsar uses two separate host values for video URLs — one for local services and one for external consumers:

| Variable | Used By | Appears In | Purpose |
| --- | --- | --- | --- |
| `RTSP_HOST` | Bridge, detector, overlay | `c4.json` `rtsp_url` | Where local services connect to the RTSP server |
| `ADVERTISE_HOST` | Registry → Overwatch API | Overwatch entity `video_config` | Where external consumers (GCS UI, etc.) connect |

The registry **never** sends `fleet.yaml` video_config to Overwatch directly. It generates a separate set of advertised endpoints using `ADVERTISE_HOST`:

```yaml
# What Overwatch receives (auto-generated from ADVERTISE_HOST):
stream_url:   rtsp://{ADVERTISE_HOST}:{port}/{entity_id}
overlay_url:  rtsp://{ADVERTISE_HOST}:{port}/{entity_id}/pulsar
webrtc_url:   http://{ADVERTISE_HOST}:8889/{entity_id}/pulsar
hls_url:      http://{ADVERTISE_HOST}:8888/{entity_id}/pulsar

# What c4.json stores (from RTSP_HOST, for local use):
rtsp_url:     rtsp://{RTSP_HOST}:{port}/{entity_id}
```

**Set both explicitly.** Auto-discovery (`ADVERTISE_HOST` default) picks the first non-loopback IPv4, which can be wrong on multi-NIC hosts, VPNs, or containers.

**MediaMTX is required for browser video (WebRTC/HLS).** Without it, Pulsar falls back to an embedded RTSP-only server that supports TCP clients (ffplay, VLC) but not WebRTC or HLS. For Overwatch UI integration, run MediaMTX alongside Pulsar:

```bash
# Install (macOS)
task setup:mediamtx          # or: brew install mediamtx

# Dev: terminal 1 — MediaMTX
task mediamtx

# Dev: terminal 2 — Pulsar with detection
ADVERTISE_HOST=localhost task dev:detection

# Open WebRTC player in browser
task test:webrtc              # → http://localhost:8889/{entity_id}
```

For deployment, MediaMTX runs as a sidecar (see `docker-compose.yaml`):

```bash
# Deployment (private network, MediaMTX sidecar)
RTSP_HOST=mediamtx ADVERTISE_HOST=10.0.1.50 MEDIAMTX_API_URL=http://mediamtx:9997 ./pulsar
```

### NATS Subjects

| Subject | Description |
| --- | --- |
| `constellation.telemetry.{entity_id}.heartbeat` | Heartbeat messages |
| `constellation.telemetry.{entity_id}.globalpositionint` | GPS position |
| `constellation.telemetry.{entity_id}.attitude` | Pitch/roll/yaw |
| `constellation.telemetry.{entity_id}.vfr_hud` | HUD data |
| `constellation.telemetry.{entity_id}.systemstatus` | Battery, sensors |

### KV Store

Bucket: `CONSTELLATION_GLOBAL_STATE`
Key pattern: `{entity_id}.mavlink`

Device state is **aggregated** — each message type merges into the existing state rather than overwriting it.

## Project Structure

```text
pulsar/
├── cmd/
│   └── microlith/
│       └── main.go                # Entry point, guided setup, sync loop
├── config/
│   ├── fleet.example.yaml         # Example config with drone + robot + sensor
│   ├── fleet.yaml                 # Fleet config (user-authored)
│   └── c4.json                    # State file (machine-generated, gitignored)
├── pkg/
│   ├── services/
│   │   ├── relay/
│   │   │   └── relay.go           # MAVLink UDP listeners, frame handling
│   │   ├── publisher/
│   │   │   └── publisher.go       # NATS JetStream + KV publishing
│   │   ├── registry/
│   │   │   ├── registry.go        # Overwatch API registration, reconciliation
│   │   │   └── registry_test.go   # Registration unit tests
│   │   ├── detector/
│   │   │   ├── detector.go        # ONNX/YOLO inference (build-tag gated)
│   │   │   └── detector_noop.go   # No-op stub when detection disabled
│   │   ├── video/
│   │   │   ├── server.go          # RTSP server (MediaMTX or embedded)
│   │   │   ├── bridge.go          # Video stream bridge
│   │   │   ├── bridge_device.go   # Local camera capture (CGO-gated)
│   │   │   ├── h264_encoder.go    # x264 H264 encoding
│   │   │   └── overlay.go         # Detection overlay rendering
│   │   └── logger/
│   │       └── logger.go          # Zap-based structured logging
│   └── shared/
│       ├── config.go              # Fleet/C4 types, YAML unmarshaling, port resolver
│       ├── client.go              # Overwatch HTTP REST client
│       └── network.go             # NIC priority ranking, ADVERTISE_HOST discovery
├── internal/
│   └── x264-go/                   # Local fork (IDR keyframe fix for WebRTC)
├── docs/
│   └── CGO_FREE_ARCHITECTURE.md   # CGO elimination migration plan
├── scripts/
│   ├── mavlink-relay.sh           # Synthetic MAVLink integration test
│   └── export_yolo26.py           # YOLO26 ONNX model export
├── data/                          # ONNX models (gitignored)
├── .env.example                   # Environment template
├── Dockerfile                     # Multi-stage Alpine build (CGO_ENABLED=0)
├── Taskfile.yml                   # Task runner commands
└── .goreleaser.yaml               # Release configuration
```

## Docker

```bash
# Build
task docker-build

# Run (mount .env and config)
docker run --env-file .env -v $(pwd)/config:/app/config pulsar:latest

# Or with Docker Compose
task docker-run
```

## Development

```bash
# Build binary
task build

# Run in dev mode
task dev

# Run synthetic MAVLink test
task test-relay

# Run tests
go test ./...

# Clean
task clean
```

## License

This project is licensed under the [MIT License](LICENSE).
