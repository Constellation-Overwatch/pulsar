# CGO-Free Architecture Plan for Pulsar

## Goal

Eliminate CGO from the Pulsar build to enable:
- `CGO_ENABLED=0` static binaries with trivial cross-compilation
- Single `GOOS=linux GOARCH=arm64 go build` from macOS (no ARM64 C toolchain)
- Lightweight Docker multi-arch builds (no gcc, no OpenCV dev packages)
- Portable deployment on Raspberry Pi, Jetson, and Apple Silicon

## Current CGO Dependencies

All CGO usage is isolated behind `//go:build detection`:

| File | CGO Library | Usage |
|------|------------|-------|
| `detector/detector.go` | **GoCV** (OpenCV) | `OpenVideoCapture` (RTSP read), `Resize`, `CvtColor`, `ConvertTo`, `Split`, `IMEncode`, `Rectangle`, `PutText` |
| `detector/detector.go` | **onnxruntime_go** | ONNX model loading, tensor creation, inference |
| `video/bridge_device.go` | **GoCV** | `OpenVideoCapture` (camera), `Read`, `IMEncodeWithParams` |
| `video/overlay.go` | **GoCV** + **x264-go** | `ToImage` (Mat conversion), H264 encoding |
| `video/h264_encoder.go` | **x264-go** (libx264) | H264 encoding from `image.Image` |

## Migration Plan

### Phase 1: Replace GoCV Image Processing (Medium effort)

**Replace with:** `disintegration/imaging` + `image/draw` + `golang.org/x/image/font`

GoCV is used for three categories:

#### 1a. Preprocessing (detector.go: `preprocess()`)
Current: `gocv.Resize` + `gocv.CvtColor` + `gocv.ConvertTo` + `gocv.Split`

Replace with pure Go:
```go
import "github.com/disintegration/imaging"

func preprocess(img image.Image) []float32 {
    // Letterbox resize
    h, w := img.Bounds().Dy(), img.Bounds().Dx()
    scale := float64(inputSize) / math.Max(float64(h), float64(w))
    newW, newH := int(float64(w)*scale), int(float64(h)*scale)
    resized := imaging.Resize(img, newW, newH, imaging.Linear)

    // Paste onto gray canvas (114,114,114)
    padded := imaging.New(inputSize, inputSize, color.NRGBA{114, 114, 114, 255})
    padded = imaging.Paste(padded, resized, image.Pt((inputSize-newW)/2, (inputSize-newH)/2))

    // RGB normalize to NCHW float32 (Go images are already RGB, no BGR conversion needed)
    result := make([]float32, 3*inputSize*inputSize)
    for y := 0; y < inputSize; y++ {
        for x := 0; x < inputSize; x++ {
            r, g, b, _ := padded.At(x, y).RGBA()
            idx := y*inputSize + x
            result[0*inputSize*inputSize+idx] = float32(r>>8) / 255.0
            result[1*inputSize*inputSize+idx] = float32(g>>8) / 255.0
            result[2*inputSize*inputSize+idx] = float32(b>>8) / 255.0
        }
    }
    return result
}
```

#### 1b. Bounding Box Drawing (detector.go: `drawDetections()`)
Current: `gocv.Rectangle` + `gocv.PutText`

Replace with:
```go
import (
    "image/draw"
    "golang.org/x/image/font"
    "golang.org/x/image/font/opentype"
    "golang.org/x/image/font/gofont/goregular"
)
```
- Draw rectangles with `image/draw.Draw()` (4 thin filled rects for border)
- Render text with `opentype.NewFace()` + `font.Drawer`
- Or use `github.com/pbnjay/pixfont` for minimal bitmap font (smaller binary)

#### 1c. Mat.ToImage() calls
Already returns `image.Image` — when GoCV is removed, frames will natively be `image.Image` from the capture source. No conversion needed.

### Phase 2: Replace GoCV Video Capture (Medium effort)

#### 2a. Camera Capture (bridge_device.go)
**Linux (Pi/Jetson):** Replace with `github.com/vladimirvivien/go4vl`
- Pure Go V4L2 via syscalls, no CGO
- Request MJPEG format from camera hardware (most USB cameras support it natively)
- Decode JPEG frames with `image/jpeg` when needed for inference

**macOS:** Use ffmpeg subprocess
```go
cmd := exec.CommandContext(ctx, "ffmpeg",
    "-f", "avfoundation", "-i", device,
    "-f", "rawvideo", "-pix_fmt", "rgb24",
    "-s", "1280x720", "-r", "15",
    "pipe:1")
// Read raw RGB frames from stdout
```

#### 2b. RTSP Stream Reading (detector.go: `gocv.OpenVideoCapture(rtspURL)`)
**Replace with:** `gortsplib/v5` client (already a dependency)
- Connect as RTSP reader, receive RTP packets
- For H264 sources: decode NALUs → raw frames via ffmpeg subprocess or software decoder
- For MJPEG sources: receive JPEG frames directly (no decode needed, just `image/jpeg.Decode`)

### Phase 3: Replace ONNX Runtime (Medium effort)

**Replace with:** `github.com/shota3506/onnxruntime-purego`

This uses `ebitengine/purego` to call the ONNX Runtime C API via `dlopen` at runtime. **No CGO at build time.**

```go
// Build: CGO_ENABLED=0 go build
// Runtime: needs libonnxruntime.so alongside binary
import "github.com/shota3506/onnxruntime-purego/onnxruntime"

rt, _ := onnxruntime.NewRuntime("/usr/lib/libonnxruntime.so")
session, _ := rt.NewSession(modelPath, inputNames, outputNames)
session.Run(inputTensors, outputTensors)
```

**Trade-off:** Still needs `libonnxruntime.so` at runtime, but the Go binary compiles with `CGO_ENABLED=0`. Cross-compilation becomes trivial — just ship the target-arch `.so` file.

**For hardware acceleration (Jetson):**
- Use sidecar pattern with NVIDIA Triton Inference Server
- Triton runs TensorRT backend, Go binary communicates via gRPC
- Go gRPC client: `github.com/triton-inference-server/client`

### Phase 4: Replace x264-go H264 Encoding (Low effort)

**Replace with:** ffmpeg subprocess

```go
cmd := exec.CommandContext(ctx, "ffmpeg",
    "-f", "rawvideo", "-pix_fmt", "rgb24",
    "-s", "1280x720", "-r", "15", "-i", "pipe:0",
    "-c:v", "libx264", "-preset", "ultrafast",
    "-tune", "zerolatency", "-profile:v", "baseline",
    "-f", "h264", "pipe:1")
// Write raw frames to stdin, read H264 NALUs from stdout
```

**Platform-specific hardware encoding:**
- Raspberry Pi: `-c:v h264_v4l2m2m` (V4L2 M2M hardware encoder)
- Jetson: `-c:v h264_nvenc` (NVENC)
- macOS: `-c:v h264_videotoolbox` (VideoToolbox)

## Target Architecture

```
┌──────────────────────────────────────────────┐
│           Pulsar Go Binary                   │
│         CGO_ENABLED=0                        │
│                                              │
│  ┌────────────┐  ┌────────────────────────┐  │
│  │ go4vl      │  │ gortsplib/v5           │  │
│  │ V4L2 cam   │  │ RTSP read/write        │  │
│  │ (pure Go)  │  │ (pure Go)              │  │
│  └─────┬──────┘  └──────┬─────────────────┘  │
│        │                │                    │
│  ┌─────▼────────────────▼─────────────────┐  │
│  │  disintegration/imaging (pure Go)      │  │
│  │  letterbox + normalize + draw          │  │
│  └──────────────┬─────────────────────────┘  │
│                 │                            │
│  ┌──────────────▼─────────────────────────┐  │
│  │  onnxruntime-purego                    │  │
│  │  dlopen(libonnxruntime) at runtime     │  │
│  │  YOLO inference                        │  │
│  └────────────────────────────────────────┘  │
└──────────────┬───────────────────────────────┘
               │ subprocess (stdin/stdout pipes)
       ┌───────▼───────┐
       │    ffmpeg      │
       │  H264 encode   │  platform HW accel
       │  camera (mac)  │  auto-detected
       └───────────────┘
```

**Runtime dependencies (shipped alongside binary):**
- `libonnxruntime.so` / `.dylib` (for detection)
- `ffmpeg` (for H264 encoding, macOS camera)

**Build output:** Single static Go binary, ~15-25MB

## Docker Multi-Arch (Simplified)

```dockerfile
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS TARGETARCH
COPY . /src
WORKDIR /src
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -o /pulsar ./cmd/microlith

FROM alpine:3.21
RUN apk add --no-cache ca-certificates ffmpeg
COPY --from=builder /pulsar /usr/local/bin/pulsar
COPY libs/${TARGETARCH}/libonnxruntime.so /usr/local/lib/
RUN ldconfig /usr/local/lib
ENTRYPOINT ["pulsar"]
```

No gcc, no OpenCV packages, no QEMU emulation needed for cross-arch.

## Migration Order (Recommended)

1. **Phase 3** (onnxruntime-purego) — Lowest risk, same runtime behavior, biggest build simplification
2. **Phase 1** (pure Go image processing) — Medium effort, eliminates OpenCV entirely
3. **Phase 4** (ffmpeg subprocess H264) — Low effort, replaces x264-go CGO
4. **Phase 2** (pure Go video capture) — Platform-specific work, do last

Each phase can be done independently. The `//go:build detection` tag system means the non-detection build is already CGO-free. The phases above progressively make the detection build CGO-free too.

## Performance Expectations

| Component | CGO (current) | Pure Go/purego | Delta |
|-----------|--------------|----------------|-------|
| Image preprocessing | ~2ms (OpenCV) | ~8-15ms (imaging) | Acceptable at 3fps inference rate |
| ONNX inference | ~50-150ms (Pi 5) | Same (same runtime) | No change |
| H264 encoding | ~3-5ms (x264) | ~3-5ms (ffmpeg subprocess) | ~0.5ms IPC overhead |
| JPEG encoding | ~1ms (libjpeg-turbo) | ~5-10ms (image/jpeg) | Only for MJPEG fallback |
| Camera capture | ~1ms (OpenCV) | ~1ms (go4vl/V4L2) | No change |
