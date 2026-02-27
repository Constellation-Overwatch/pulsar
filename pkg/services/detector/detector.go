//go:build detection

package detector

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/publisher"
	"github.com/Constellation-Overwatch/pulsar/pkg/services/video"
	"github.com/Constellation-Overwatch/pulsar/pkg/shared"
	"github.com/google/uuid"
	ort "github.com/yalue/onnxruntime_go"
	"gocv.io/x/gocv"
)

const (
	inputSize        = 640
	confThreshold    = 0.35
	nmsThreshold     = 0.45
	trackMaxAge      = 5 * time.Second
	trackMatchDist   = 0.08 // 8% of frame size
	processEveryN    = 5
	maxE2EDetections = 300 // YOLO26 e2e head max detections per image
)

var defaultClasses = []string{"drone", "quadcopter", "airplane", "helicopter", "bird", "person"}

// cocoClasses are the 80 COCO class names used by pretrained YOLO26 models.
var cocoClasses = []string{
	"person", "bicycle", "car", "motorcycle", "airplane", "bus", "train", "truck",
	"boat", "traffic light", "fire hydrant", "stop sign", "parking meter", "bench",
	"bird", "cat", "dog", "horse", "sheep", "cow", "elephant", "bear", "zebra",
	"giraffe", "backpack", "umbrella", "handbag", "tie", "suitcase", "frisbee",
	"skis", "snowboard", "sports ball", "kite", "baseball bat", "baseball glove",
	"skateboard", "surfboard", "tennis racket", "bottle", "wine glass", "cup",
	"fork", "knife", "spoon", "bowl", "banana", "apple", "sandwich", "orange",
	"broccoli", "carrot", "hot dog", "pizza", "donut", "cake", "chair", "couch",
	"potted plant", "bed", "dining table", "toilet", "tv", "laptop", "mouse",
	"remote", "keyboard", "cell phone", "microwave", "oven", "toaster", "sink",
	"refrigerator", "book", "clock", "vase", "scissors", "teddy bear", "hair drier",
	"toothbrush",
}

// DetectionEvent is published to NATS for each significant detection.
type DetectionEvent struct {
	EntityID   string    `json:"entity_id"`
	OrgID      string    `json:"org_id"`
	TrackID    string    `json:"track_id"`
	Label      string    `json:"label"`
	Confidence float64   `json:"confidence"`
	X1         float64   `json:"x1"`
	Y1         float64   `json:"y1"`
	X2         float64   `json:"x2"`
	Y2         float64   `json:"y2"`
	Timestamp  time.Time `json:"timestamp"`
}

// EntityDetectionState is the KV state for an entity running detection.
type EntityDetectionState struct {
	EntityID   string           `json:"entity_id"`
	Status     string           `json:"status"`
	IsLive     bool             `json:"is_live"`
	Detections []DetectionEvent `json:"detections"`
	FrameCount int64            `json:"frame_count"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

type trackedObject struct {
	ID         string
	Label      string
	Confidence float64
	CX, CY    float64
	PrevCX     float64
	PrevCY     float64
	X1, Y1     float64
	X2, Y2     float64
	FirstSeen  time.Time
	LastSeen   time.Time
	FrameCount int
}

// StartDetector starts YOLOE detection on video sources for each entity with video config.
// For device cameras, the detector opens them directly (no RTSP round-trip).
// For RTSP sources, it reads from the RTSP URL.
// modelPath is the path to the ONNX model file, onnxLibPath is the optional ONNX runtime library path.
// overlayWriters maps entity_id → OverlayWriter for publishing annotated frames.
func StartDetector(ctx context.Context, entities []shared.EntityState, orgID string, pub *publisher.OverwatchPublisher, modelPath, onnxLibPath string, overlayWriters map[string]*video.OverlayWriter) {
	if onnxLibPath != "" {
		ort.SetSharedLibraryPath(onnxLibPath)
	}
	if err := ort.InitializeEnvironment(); err != nil {
		logger.Errorf("[detector] failed to init ONNX runtime: %v (skipping detection)", err)
		return
	}

	started := 0
	for _, entity := range entities {
		// Resolve video source: prefer device camera (direct) over RTSP (round-trip)
		videoSource := resolveVideoSource(entity)
		if videoSource == "" {
			continue
		}
		overlay := overlayWriters[entity.EntityID]
		go runDetectorForEntity(ctx, entity, videoSource, orgID, modelPath, pub, overlay)
		started++
	}
	if started > 0 {
		logger.Infof("[detector] started %d YOLOE detector(s)", started)
	}
}

// resolveVideoSource returns the best video source for the detector.
// Device cameras are preferred (direct access, no RTSP round-trip).
// Falls back to the RTSP URL for network sources.
func resolveVideoSource(entity shared.EntityState) string {
	if dev := video.ResolveDeviceSource(entity); dev != "" {
		return dev
	}
	return entity.RTSPURL
}

func runDetectorForEntity(ctx context.Context, entity shared.EntityState, videoSource, orgID, modelPath string, pub *publisher.OverwatchPublisher, overlay *video.OverlayWriter) {
	isDevice := video.ResolveDeviceSource(entity) != ""
	if isDevice {
		logger.Infof("[detector] %s: opening device %s directly (no RTSP round-trip)", entity.Name, videoSource)
	} else {
		logger.Infof("[detector] %s: opening RTSP %s", entity.Name, videoSource)
	}

	// Retry loop: device or RTSP source may still be starting up.
	const maxRetries = 10
	const retryInterval = 2 * time.Second

	var cap *gocv.VideoCapture
	var err error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		cap, err = gocv.OpenVideoCapture(videoSource)
		if err == nil {
			break
		}
		if attempt == maxRetries {
			logger.Errorf("[detector] failed to open %s after %d attempts: %v", videoSource, maxRetries, err)
			return
		}
		logger.Warnf("[detector] source not ready for %s, retrying (%d/%d)...", entity.Name, attempt, maxRetries)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryInterval):
		}
	}
	defer cap.Close()

	// Set camera resolution for direct device capture
	if isDevice {
		cap.Set(gocv.VideoCaptureFrameWidth, 1280)
		cap.Set(gocv.VideoCaptureFrameHeight, 720)
	}

	// Detect model format: "e2e" for YOLO26 (default), "legacy" for YOLOv8/YOLO11
	modelFormat := "e2e"
	if f := os.Getenv("MODEL_FORMAT"); f != "" {
		modelFormat = f
	}
	// Model source: "hf" (HuggingFace community ONNX, default) or "ultralytics"
	modelSource := os.Getenv("MODEL_SOURCE")
	if modelSource == "" {
		modelSource = "hf"
	}

	inputShape := ort.NewShape(1, 3, int64(inputSize), int64(inputSize))
	input, err := ort.NewEmptyTensor[float32](inputShape)
	if err != nil {
		logger.Errorf("[detector] input tensor error: %v", err)
		return
	}
	defer input.Destroy()

	var session *ort.AdvancedSession
	var postprocessFn func() []rawDetection
	var numClasses int

	switch {
	case modelFormat == "e2e" && modelSource == "hf":
		// HuggingFace community: pixel_values -> logits (1,300,80) + pred_boxes (1,300,4)
		numClasses = len(cocoClasses)
		logitsOutput, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(maxE2EDetections), int64(numClasses)))
		if err != nil {
			logger.Errorf("[detector] logits tensor error: %v", err)
			return
		}
		defer logitsOutput.Destroy()

		boxesOutput, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(maxE2EDetections), 4))
		if err != nil {
			logger.Errorf("[detector] boxes tensor error: %v", err)
			return
		}
		defer boxesOutput.Destroy()

		session, err = ort.NewAdvancedSession(modelPath,
			[]string{"pixel_values"}, []string{"logits", "pred_boxes"},
			[]ort.ArbitraryTensor{input}, []ort.ArbitraryTensor{logitsOutput, boxesOutput}, nil)
		if err != nil {
			logger.Errorf("[detector] ONNX session error: %v", err)
			return
		}

		nc := numClasses
		postprocessFn = func() []rawDetection {
			return postprocessHF(logitsOutput.GetData(), boxesOutput.GetData(), nc)
		}

	case modelFormat == "e2e":
		// Ultralytics export: images -> output0 (1,300,6) [x1,y1,x2,y2,score,class_id]
		numClasses = len(cocoClasses)
		output, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(maxE2EDetections), 6))
		if err != nil {
			logger.Errorf("[detector] output tensor error: %v", err)
			return
		}
		defer output.Destroy()

		session, err = ort.NewAdvancedSession(modelPath,
			[]string{"images"}, []string{"output0"},
			[]ort.ArbitraryTensor{input}, []ort.ArbitraryTensor{output}, nil)
		if err != nil {
			logger.Errorf("[detector] ONNX session error: %v", err)
			return
		}

		postprocessFn = func() []rawDetection {
			return postprocessE2E(output.GetData())
		}

	default:
		// Legacy YOLOv8/YOLO11: images -> output0 (1, nc+4, 8400) — transposed, needs NMS
		if n := os.Getenv("MODEL_NUM_CLASSES"); n != "" {
			numClasses, _ = strconv.Atoi(n)
		}
		if numClasses <= 0 {
			numClasses = len(defaultClasses)
		}
		output, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(4+numClasses), 8400))
		if err != nil {
			logger.Errorf("[detector] output tensor error: %v", err)
			return
		}
		defer output.Destroy()

		session, err = ort.NewAdvancedSession(modelPath,
			[]string{"images"}, []string{"output0"},
			[]ort.ArbitraryTensor{input}, []ort.ArbitraryTensor{output}, nil)
		if err != nil {
			logger.Errorf("[detector] ONNX session error: %v", err)
			return
		}

		nc := numClasses
		postprocessFn = func() []rawDetection {
			return postprocess(output.GetData(), nc)
		}
	}
	defer session.Destroy()

	logger.Infof("[detector] %s: model loaded (source=%s, format=%s, classes=%d)", entity.Name, modelSource, modelFormat, numClasses)

	// Tracking state
	var mu sync.Mutex
	tracks := make(map[string]*trackedObject)

	// Reclaim track IDs from previous KV state for continuity across restarts
	if existing, err := pub.GetDetections(entity.EntityID); err == nil && existing != nil {
		for trackID, obj := range existing {
			tracks[trackID] = &trackedObject{
				ID:         trackID,
				Label:      obj.Label,
				Confidence: obj.Confidence,
				CX:         obj.CX,
				CY:         obj.CY,
				PrevCX:     obj.CX,
				PrevCY:     obj.CY,
				X1:         obj.BBox.X1,
				Y1:         obj.BBox.Y1,
				X2:         obj.BBox.X2,
				Y2:         obj.BBox.Y2,
				FirstSeen:  parseTimeOrNow(obj.FirstSeen),
				LastSeen:   time.Now().UTC(),
				FrameCount: obj.FrameCount,
			}
		}
		if len(tracks) > 0 {
			logger.Infof("[detector] reclaimed %d track(s) from KV for %s", len(tracks), entity.Name)
		}
	}

	frame := gocv.NewMat()
	defer frame.Close()
	var frameCount int64
	var lastDetections []rawDetection // cached from last YOLO run

	// KV update ticker (throttle to 1/sec)
	kvTicker := time.NewTicker(time.Second)
	defer kvTicker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-kvTicker.C:
				mu.Lock()
				detState := buildDetectionsForKV(tracks, frameCount)
				mu.Unlock()
				if err := pub.UpdateDetections(entity.EntityID, detState); err != nil {
					logger.Errorf("[detector] kv error for %s: %v", entity.Name, err)
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if ok := cap.Read(&frame); !ok || frame.Empty() {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		frameCount++

		// Run YOLO inference every Nth frame only
		if frameCount%processEveryN == 0 {
			preprocessed := preprocess(frame)
			copy(input.GetData(), preprocessed)

			if err := session.Run(); err != nil {
				logger.Errorf("[detector] inference error: %v", err)
			} else {
				detections := postprocessFn()
				lastDetections = detections

				mu.Lock()
				now := time.Now().UTC()
				pruneStale(tracks, now)

				for _, d := range detections {
					track := matchOrCreateTrack(&tracks, d, now)
					track.FrameCount++

					if track.FrameCount <= 2 || track.FrameCount%5 == 0 {
						event := DetectionEvent{
							EntityID:   entity.EntityID,
							OrgID:      orgID,
							TrackID:    track.ID,
							Label:      d.label,
							Confidence: d.confidence,
							X1:         d.x1,
							Y1:         d.y1,
							X2:         d.x2,
							Y2:         d.y2,
							Timestamp:  now,
						}
						go pub.PublishDetection(orgID, entity.EntityID, track.ID, event)
					}
				}
				mu.Unlock()
			}
		}

		// Write EVERY frame to overlay for smooth video (15fps).
		// Draw cached detections from the last YOLO run.
		// At 15fps, x264 produces keyframes every ~1s so WebRTC
		// consumers can start decoding immediately.
		if overlay != nil {
			if len(lastDetections) > 0 {
				annotated := frame.Clone()
				drawDetections(&annotated, lastDetections)
				if err := overlay.WriteFrame(annotated); err != nil {
					logger.Errorf("[detector] overlay write error for %s: %v", entity.Name, err)
				}
				annotated.Close()
			} else {
				if err := overlay.WriteFrame(frame); err != nil {
					logger.Errorf("[detector] overlay write error for %s: %v", entity.Name, err)
				}
			}
		}
	}
}

// --- Preprocessing ---

func preprocess(img gocv.Mat) []float32 {
	// Letterbox resize to inputSize x inputSize
	h, w := img.Rows(), img.Cols()
	scale := float64(inputSize) / math.Max(float64(h), float64(w))
	newH, newW := int(float64(h)*scale), int(float64(w)*scale)

	resized := gocv.NewMat()
	defer resized.Close()
	gocv.Resize(img, &resized, image.Point{X: newW, Y: newH}, 0, 0, gocv.InterpolationLinear)

	padded := gocv.NewMatWithSize(inputSize, inputSize, gocv.MatTypeCV8UC3)
	defer padded.Close()
	padded.SetTo(gocv.NewScalar(114, 114, 114, 0))

	padY, padX := (inputSize-newH)/2, (inputSize-newW)/2
	roi := padded.Region(image.Rect(padX, padY, padX+newW, padY+newH))
	resized.CopyTo(&roi)
	roi.Close()

	// BGR -> RGB
	rgb := gocv.NewMat()
	defer rgb.Close()
	gocv.CvtColor(padded, &rgb, gocv.ColorBGRToRGB)

	// Float32, normalize to [0, 1]
	floatMat := gocv.NewMat()
	defer floatMat.Close()
	rgb.ConvertTo(&floatMat, gocv.MatTypeCV32F)
	floatMat.DivideFloat(255.0)

	// HWC -> NCHW
	channels := gocv.Split(floatMat)
	result := make([]float32, 3*inputSize*inputSize)
	for c, ch := range channels {
		data, _ := ch.DataPtrFloat32()
		copy(result[c*inputSize*inputSize:], data)
		ch.Close()
	}
	return result
}

// --- Postprocessing ---

type rawDetection struct {
	x1, y1, x2, y2 float64
	confidence      float64
	label           string
	classID         int
}

func postprocess(data []float32, numClasses int) []rawDetection {
	const numCandidates = 8400
	stride := 4 + numClasses

	var detections []rawDetection
	for i := 0; i < numCandidates; i++ {
		// Transposed layout: data[feature * 8400 + candidate]
		cx := float64(data[0*numCandidates+i])
		cy := float64(data[1*numCandidates+i])
		w := float64(data[2*numCandidates+i])
		h := float64(data[3*numCandidates+i])

		maxConf := float64(0)
		maxClass := 0
		for c := 0; c < numClasses; c++ {
			conf := float64(data[(4+c)*numCandidates+i])
			if conf > maxConf {
				maxConf = conf
				maxClass = c
			}
		}

		if maxConf < confThreshold {
			continue
		}

		// Normalize to [0, 1]
		x1 := (cx - w/2) / float64(inputSize)
		y1 := (cy - h/2) / float64(inputSize)
		x2 := (cx + w/2) / float64(inputSize)
		y2 := (cy + h/2) / float64(inputSize)

		label := fmt.Sprintf("class_%d", maxClass)
		if maxClass < len(defaultClasses) {
			label = defaultClasses[maxClass]
		}

		detections = append(detections, rawDetection{
			x1: x1, y1: y1, x2: x2, y2: y2,
			confidence: maxConf,
			label:      label,
			classID:    maxClass,
		})
	}

	_ = stride // used for documentation
	return nms(detections)
}

func nms(dets []rawDetection) []rawDetection {
	if len(dets) == 0 {
		return nil
	}
	// Sort by confidence descending (simple selection)
	for i := 0; i < len(dets); i++ {
		for j := i + 1; j < len(dets); j++ {
			if dets[j].confidence > dets[i].confidence {
				dets[i], dets[j] = dets[j], dets[i]
			}
		}
	}
	var keep []rawDetection
	suppressed := make([]bool, len(dets))
	for i := range dets {
		if suppressed[i] {
			continue
		}
		keep = append(keep, dets[i])
		for j := i + 1; j < len(dets); j++ {
			if suppressed[j] || dets[j].classID != dets[i].classID {
				continue
			}
			if iou(dets[i], dets[j]) > nmsThreshold {
				suppressed[j] = true
			}
		}
	}
	return keep
}

func iou(a, b rawDetection) float64 {
	x1 := math.Max(a.x1, b.x1)
	y1 := math.Max(a.y1, b.y1)
	x2 := math.Min(a.x2, b.x2)
	y2 := math.Min(a.y2, b.y2)
	inter := math.Max(0, x2-x1) * math.Max(0, y2-y1)
	areaA := (a.x2 - a.x1) * (a.y2 - a.y1)
	areaB := (b.x2 - b.x1) * (b.y2 - b.y1)
	union := areaA + areaB - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

// postprocessE2E parses YOLO26 end-to-end output: (1, 300, 6).
// Each detection: [x1, y1, x2, y2, confidence, class_id] in pixel coords (0-640).
// No NMS needed — the model handles it internally.
func postprocessE2E(data []float32) []rawDetection {
	var detections []rawDetection
	for i := 0; i < maxE2EDetections; i++ {
		offset := i * 6
		if offset+5 >= len(data) {
			break
		}

		score := float64(data[offset+4])
		if score < confThreshold {
			continue
		}

		// Normalize pixel coords to [0, 1]
		x1 := float64(data[offset]) / float64(inputSize)
		y1 := float64(data[offset+1]) / float64(inputSize)
		x2 := float64(data[offset+2]) / float64(inputSize)
		y2 := float64(data[offset+3]) / float64(inputSize)
		classID := int(data[offset+5])

		label := fmt.Sprintf("class_%d", classID)
		if classID >= 0 && classID < len(cocoClasses) {
			label = cocoClasses[classID]
		}

		detections = append(detections, rawDetection{
			x1: x1, y1: y1, x2: x2, y2: y2,
			confidence: score,
			label:      label,
			classID:    classID,
		})
	}
	return detections
}

// postprocessHF parses HuggingFace community ONNX output: logits (1,300,80) + pred_boxes (1,300,4).
// Logits are raw (need sigmoid). Boxes are normalized [cx, cy, w, h].
func postprocessHF(logitsData, boxesData []float32, numClasses int) []rawDetection {
	var detections []rawDetection
	for i := 0; i < maxE2EDetections; i++ {
		logitsOffset := i * numClasses
		boxOffset := i * 4

		if logitsOffset+numClasses > len(logitsData) || boxOffset+4 > len(boxesData) {
			break
		}

		// Find max class via sigmoid(logit)
		maxConf := float64(0)
		maxClass := 0
		for c := 0; c < numClasses; c++ {
			logit := float64(logitsData[logitsOffset+c])
			conf := 1.0 / (1.0 + math.Exp(-logit))
			if conf > maxConf {
				maxConf = conf
				maxClass = c
			}
		}

		if maxConf < confThreshold {
			continue
		}

		// pred_boxes: [cx, cy, w, h] normalized (0-1)
		cx := float64(boxesData[boxOffset])
		cy := float64(boxesData[boxOffset+1])
		w := float64(boxesData[boxOffset+2])
		h := float64(boxesData[boxOffset+3])

		x1 := cx - w/2
		y1 := cy - h/2
		x2 := cx + w/2
		y2 := cy + h/2

		label := fmt.Sprintf("class_%d", maxClass)
		if maxClass >= 0 && maxClass < len(cocoClasses) {
			label = cocoClasses[maxClass]
		}

		detections = append(detections, rawDetection{
			x1: x1, y1: y1, x2: x2, y2: y2,
			confidence: maxConf,
			label:      label,
			classID:    maxClass,
		})
	}
	return detections
}

// --- Tracking ---

func matchOrCreateTrack(tracks *map[string]*trackedObject, d rawDetection, now time.Time) *trackedObject {
	cx := (d.x1 + d.x2) / 2
	cy := (d.y1 + d.y2) / 2

	var best *trackedObject
	bestDist := trackMatchDist

	for _, t := range *tracks {
		if t.Label != d.label {
			continue
		}
		dist := math.Sqrt(math.Pow(cx-t.CX, 2) + math.Pow(cy-t.CY, 2))
		if dist < bestDist {
			bestDist = dist
			best = t
		}
	}

	if best != nil {
		best.PrevCX = best.CX
		best.PrevCY = best.CY
		best.CX = cx
		best.CY = cy
		best.Confidence = d.confidence
		best.X1 = d.x1
		best.Y1 = d.y1
		best.X2 = d.x2
		best.Y2 = d.y2
		best.LastSeen = now
		return best
	}

	t := &trackedObject{
		ID:         uuid.NewString(),
		Label:      d.label,
		Confidence: d.confidence,
		CX:         cx,
		CY:         cy,
		PrevCX:     cx,
		PrevCY:     cy,
		X1:         d.x1,
		Y1:         d.y1,
		X2:         d.x2,
		Y2:         d.y2,
		FirstSeen:  now,
		LastSeen:   now,
	}
	(*tracks)[t.ID] = t
	return t
}

func pruneStale(tracks map[string]*trackedObject, now time.Time) {
	for id, t := range tracks {
		if now.Sub(t.LastSeen) > trackMaxAge {
			delete(tracks, id)
		}
	}
}

func buildDetectionsForKV(tracks map[string]*trackedObject, frameCount int64) *publisher.DetectionsState {
	objects := make(map[string]*publisher.DetectedObject, len(tracks))
	for _, t := range tracks {
		objects[t.ID] = &publisher.DetectedObject{
			Label:      t.Label,
			Confidence: t.Confidence,
			BBox: publisher.BBox{
				X1: t.X1, Y1: t.Y1,
				X2: t.X2, Y2: t.Y2,
			},
			CX:         t.CX,
			CY:         t.CY,
			DX:         t.CX - t.PrevCX,
			DY:         t.CY - t.PrevCY,
			FrameCount: t.FrameCount,
			FirstSeen:  t.FirstSeen.Format(time.RFC3339Nano),
			LastSeen:   t.LastSeen.Format(time.RFC3339Nano),
		}
	}
	return &publisher.DetectionsState{
		Status:     "online",
		IsLive:     true,
		FrameCount: frameCount,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		Objects:    objects,
	}
}

func buildEntityDetectionState(entityID string, tracks map[string]*trackedObject, frameCount int64) EntityDetectionState {
	var dets []DetectionEvent
	for _, t := range tracks {
		dets = append(dets, DetectionEvent{
			EntityID:   entityID,
			TrackID:    t.ID,
			Label:      t.Label,
			Confidence: t.Confidence,
			Timestamp:  t.LastSeen,
		})
	}
	return EntityDetectionState{
		EntityID:   entityID,
		Status:     "online",
		IsLive:     true,
		Detections: dets,
		FrameCount: frameCount,
		UpdatedAt:  time.Now().UTC(),
	}
}

// --- Overlay Drawing ---

// classColors maps detection labels to BGR colors for bounding boxes.
var classColors = map[string]color.RGBA{
	"drone":       {R: 0, G: 0, B: 255, A: 255},   // red
	"quadcopter":  {R: 0, G: 0, B: 255, A: 255},   // red
	"airplane":    {R: 255, G: 0, B: 0, A: 255},    // blue
	"helicopter":  {R: 255, G: 255, B: 0, A: 255},  // cyan
	"bird":        {R: 0, G: 255, B: 0, A: 255},    // green
	"person":      {R: 0, G: 255, B: 255, A: 255},  // yellow
}

func drawDetections(frame *gocv.Mat, detections []rawDetection) {
	h := float64(frame.Rows())
	w := float64(frame.Cols())

	for _, d := range detections {
		// Convert normalized coords to pixel coords
		x1 := int(d.x1 * w)
		y1 := int(d.y1 * h)
		x2 := int(d.x2 * w)
		y2 := int(d.y2 * h)

		c, ok := classColors[d.label]
		if !ok {
			c = color.RGBA{R: 255, G: 255, B: 255, A: 255} // white default
		}

		rect := image.Rect(x1, y1, x2, y2)
		gocv.Rectangle(frame, rect, c, 2)

		label := fmt.Sprintf("%s %.0f%%", d.label, d.confidence*100)
		gocv.PutText(frame, label, image.Pt(x1, y1-6), gocv.FontHersheyPlain, 1.2, c, 2)
	}
}

func parseTimeOrNow(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t
}
