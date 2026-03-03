package detector

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"
	"github.com/Constellation-Overwatch/pulsar/pkg/services/publisher"
	"github.com/Constellation-Overwatch/pulsar/pkg/services/video"
	"github.com/Constellation-Overwatch/pulsar/pkg/shared"
	"github.com/disintegration/imaging"
	"github.com/google/uuid"
	"github.com/shota3506/onnxruntime-purego/onnxruntime"
)

const (
	inputSize        = 640
	confThreshold    = 0.35
	nmsThreshold     = 0.45
	trackMaxAge      = 5 * time.Second
	trackMatchDist   = 0.08 // 8% of frame size
	processEveryN    = 5
	maxE2EDetections = 300 // YOLO26 e2e head max detections per image

	captureWidth  = 1280
	captureHeight = 720
	captureFPS    = 15
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
// Detection is runtime-optional: if the ONNX runtime library is not available, it logs
// a message and returns without error. Similarly, ffmpeg must be available for video capture.
func StartDetector(ctx context.Context, entities []shared.EntityState, orgID string, pub *publisher.OverwatchPublisher, modelPath, onnxLibPath string, overlayWriters map[string]*video.OverlayWriter) {
	if onnxLibPath == "" {
		logger.Info("[detector] ONNX_LIB_PATH not set, detection disabled")
		return
	}

	rt, err := onnxruntime.NewRuntime(onnxLibPath, 23)
	if err != nil {
		logger.Infof("[detector] ONNX runtime not available (%v), detection disabled", err)
		return
	}

	started := 0
	for _, entity := range entities {
		videoSource := resolveVideoSource(entity)
		if videoSource == "" {
			continue
		}
		overlay := overlayWriters[entity.EntityID]
		go runDetectorForEntity(ctx, rt, entity, videoSource, orgID, modelPath, pub, overlay)
		started++
	}
	if started > 0 {
		logger.Infof("[detector] started %d YOLOE detector(s)", started)
	}

	// Keep runtime alive until context is cancelled, then close it.
	// The goroutines above hold references to sessions created from this runtime.
	go func() {
		<-ctx.Done()
		rt.Close()
	}()
}

// resolveVideoSource returns the RTSP URL for the detector to read from.
// The video bridge publishes all sources (RTSP and device) to MediaMTX,
// so the detector always reads from the entity's RTSP path.
func resolveVideoSource(entity shared.EntityState) string {
	return entity.RTSPURL
}

func runDetectorForEntity(ctx context.Context, rt *onnxruntime.Runtime, entity shared.EntityState, videoSource, orgID, modelPath string, pub *publisher.OverwatchPublisher, overlay *video.OverlayWriter) {
	logger.Infof("[detector] %s: opening RTSP %s", entity.Name, videoSource)

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

	// Create ONNX environment and session
	env, err := rt.NewEnv("pulsar-detector", onnxruntime.LoggingLevelWarning)
	if err != nil {
		logger.Errorf("[detector] ONNX env error: %v", err)
		return
	}
	defer env.Close()

	session, err := rt.NewSession(env, modelPath, nil)
	if err != nil {
		logger.Errorf("[detector] ONNX session error: %v", err)
		return
	}
	defer session.Close()

	inputNames := session.InputNames()
	outputNames := session.OutputNames()

	var numClasses int
	var postprocessFn func(outputs map[string]*onnxruntime.Value) []rawDetection

	switch {
	case modelFormat == "e2e" && modelSource == "hf":
		// HuggingFace community: pixel_values -> logits (1,300,80) + pred_boxes (1,300,4)
		numClasses = len(cocoClasses)
		nc := numClasses
		postprocessFn = func(outputs map[string]*onnxruntime.Value) []rawDetection {
			logitsData, _, _ := onnxruntime.GetTensorData[float32](outputs[outputNames[0]])
			boxesData, _, _ := onnxruntime.GetTensorData[float32](outputs[outputNames[1]])
			return postprocessHF(logitsData, boxesData, nc)
		}

	case modelFormat == "e2e":
		// Ultralytics export: images -> output0 (1,300,6) [x1,y1,x2,y2,score,class_id]
		numClasses = len(cocoClasses)
		postprocessFn = func(outputs map[string]*onnxruntime.Value) []rawDetection {
			data, _, _ := onnxruntime.GetTensorData[float32](outputs[outputNames[0]])
			return postprocessE2E(data)
		}

	default:
		// Legacy YOLOv8/YOLO11: images -> output0 (1, nc+4, 8400) — transposed, needs NMS
		if n := os.Getenv("MODEL_NUM_CLASSES"); n != "" {
			numClasses, _ = strconv.Atoi(n)
		}
		if numClasses <= 0 {
			numClasses = len(defaultClasses)
		}
		nc := numClasses
		postprocessFn = func(outputs map[string]*onnxruntime.Value) []rawDetection {
			data, _, _ := onnxruntime.GetTensorData[float32](outputs[outputNames[0]])
			return postprocess(data, nc)
		}
	}

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

	// Outer loop: (re)connect to the RTSP source. The bridge may still be
	// starting up or the source may drop — we retry indefinitely until the
	// context is cancelled.
	const maxConnRetries = 30
	const connRetryInterval = 3 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		var reader *FrameReader
		var err error
		for attempt := 1; attempt <= maxConnRetries; attempt++ {
			reader, err = NewFrameReader(ctx, videoSource, captureWidth, captureHeight, captureFPS)
			if err == nil {
				break
			}
			if attempt == maxConnRetries {
				logger.Errorf("[detector] failed to open %s after %d attempts: %v", videoSource, maxConnRetries, err)
				return
			}
			logger.Warnf("[detector] source not ready for %s, retrying (%d/%d)...", entity.Name, attempt, maxConnRetries)
			select {
			case <-ctx.Done():
				return
			case <-time.After(connRetryInterval):
			}
		}

		// Inner loop: read frames until error/EOF, then reconnect
		for {
			select {
			case <-ctx.Done():
				reader.Close()
				return
			default:
			}

			frame, err := reader.Read()
			if err != nil {
				reader.Close()
				logger.Warnf("[detector] %s: stream interrupted (%v), reconnecting...", entity.Name, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
				break // break inner loop to reconnect in outer loop
			}
			frameCount++

		// Run YOLO inference every Nth frame only
		if frameCount%processEveryN == 0 {
			preprocessed := preprocess(frame)

			inputTensor, err := onnxruntime.NewTensorValue(rt, preprocessed, []int64{1, 3, int64(inputSize), int64(inputSize)})
			if err != nil {
				logger.Errorf("[detector] input tensor error: %v", err)
				continue
			}

			inputs := map[string]*onnxruntime.Value{
				inputNames[0]: inputTensor,
			}
			outputs, err := session.Run(ctx, inputs, onnxruntime.WithOutputNames(outputNames...))
			inputTensor.Close()

			if err != nil {
				logger.Errorf("[detector] inference error: %v", err)
			} else {
				detections := postprocessFn(outputs)
				for _, v := range outputs {
					v.Close()
				}
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
		if overlay != nil {
			if len(lastDetections) > 0 {
				annotated := drawDetections(frame, lastDetections)
				if err := overlay.WriteFrame(annotated); err != nil {
					logger.Errorf("[detector] overlay write error for %s: %v", entity.Name, err)
				}
			} else {
				if err := overlay.WriteFrame(frame); err != nil {
					logger.Errorf("[detector] overlay write error for %s: %v", entity.Name, err)
				}
			}
		}
		} // end inner frame-read loop
	} // end outer reconnect loop
}

// --- Preprocessing ---

func preprocess(img image.Image) []float32 {
	bounds := img.Bounds()
	h, w := bounds.Dy(), bounds.Dx()
	scale := float64(inputSize) / math.Max(float64(h), float64(w))
	newH, newW := int(float64(h)*scale), int(float64(w)*scale)

	// Resize to fit within inputSize x inputSize
	resized := imaging.Resize(img, newW, newH, imaging.Linear)

	// Create padded image with gray (114,114,114) fill
	padded := imaging.New(inputSize, inputSize, color.NRGBA{R: 114, G: 114, B: 114, A: 255})

	// Paste resized into center of padded
	padY, padX := (inputSize-newH)/2, (inputSize-newW)/2
	padded = imaging.Paste(padded, resized, image.Pt(padX, padY))

	// Extract NCHW float32 normalized to [0, 1]
	// Go images are already RGB, no BGR conversion needed
	result := make([]float32, 3*inputSize*inputSize)
	for y := 0; y < inputSize; y++ {
		for x := 0; x < inputSize; x++ {
			r, g, b, _ := padded.At(x, y).RGBA()
			idx := y*inputSize + x
			result[0*inputSize*inputSize+idx] = float32(r>>8) / 255.0 // R channel
			result[1*inputSize*inputSize+idx] = float32(g>>8) / 255.0 // G channel
			result[2*inputSize*inputSize+idx] = float32(b>>8) / 255.0 // B channel
		}
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

// --- Overlay Drawing (pure Go, no GoCV) ---

// classColors maps detection labels to RGB colors for bounding boxes.
var classColors = map[string]color.RGBA{
	"drone":      {R: 255, G: 0, B: 0, A: 255},     // red
	"quadcopter": {R: 255, G: 0, B: 0, A: 255},     // red
	"airplane":   {R: 0, G: 0, B: 255, A: 255},     // blue
	"helicopter": {R: 0, G: 255, B: 255, A: 255},   // cyan
	"bird":       {R: 0, G: 255, B: 0, A: 255},     // green
	"person":     {R: 255, G: 255, B: 0, A: 255},   // yellow
}

// drawDetections renders bounding boxes and labels onto the image.
// Returns a new *image.NRGBA with annotations drawn.
func drawDetections(img image.Image, detections []rawDetection) *image.NRGBA {
	bounds := img.Bounds()
	h := float64(bounds.Dy())
	w := float64(bounds.Dx())

	// Clone the image to draw on
	dst := imaging.Clone(img)

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

		// Draw bounding box using 4 thin filled rectangles (2px thick)
		thickness := 2
		uni := image.NewUniform(c)
		// Top edge
		draw.Draw(dst, image.Rect(x1, y1, x2, y1+thickness), uni, image.Point{}, draw.Over)
		// Bottom edge
		draw.Draw(dst, image.Rect(x1, y2-thickness, x2, y2), uni, image.Point{}, draw.Over)
		// Left edge
		draw.Draw(dst, image.Rect(x1, y1, x1+thickness, y2), uni, image.Point{}, draw.Over)
		// Right edge
		draw.Draw(dst, image.Rect(x2-thickness, y1, x2, y2), uni, image.Point{}, draw.Over)

		// Draw label background
		label := fmt.Sprintf("%s %.0f%%", d.label, d.confidence*100)
		labelW := len(label) * 7 // approximate character width
		labelH := 14
		bgRect := image.Rect(x1, y1-labelH-2, x1+labelW+4, y1)
		draw.Draw(dst, bgRect, uni, image.Point{}, draw.Over)

		// Draw label text using simple pixel font
		drawString(dst, label, x1+2, y1-labelH, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	}
	return dst
}

// drawString renders a string onto an NRGBA image using a minimal 5x7 bitmap font.
// This avoids any dependency on freetype, x/image/font, or external font files.
func drawString(img *image.NRGBA, s string, x, y int, c color.RGBA) {
	for _, ch := range s {
		glyph := getGlyph(byte(ch))
		for gy := 0; gy < 7; gy++ {
			for gx := 0; gx < 5; gx++ {
				if glyph[gy]&(1<<(4-gx)) != 0 {
					px, py := x+gx, y+gy
					if px >= 0 && py >= 0 && px < img.Bounds().Dx() && py < img.Bounds().Dy() {
						img.SetNRGBA(px, py, color.NRGBA{R: c.R, G: c.G, B: c.B, A: c.A})
					}
				}
			}
		}
		x += 7 // character width + spacing
	}
}

// getGlyph returns a 5x7 bitmap for basic ASCII characters.
func getGlyph(ch byte) [7]byte {
	switch {
	case ch >= 'A' && ch <= 'Z':
		return uppercaseGlyphs[ch-'A']
	case ch >= 'a' && ch <= 'z':
		return lowercaseGlyphs[ch-'a']
	case ch >= '0' && ch <= '9':
		return digitGlyphs[ch-'0']
	case ch == ' ':
		return [7]byte{}
	case ch == '%':
		return [7]byte{0x18, 0x19, 0x02, 0x04, 0x08, 0x13, 0x03}
	case ch == '_':
		return [7]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x1F}
	case ch == '.':
		return [7]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x04}
	default:
		return [7]byte{0x0A, 0x00, 0x0A, 0x00, 0x0A, 0x00, 0x0A} // checkerboard for unknown
	}
}

var uppercaseGlyphs = [26][7]byte{
	{0x04, 0x0A, 0x11, 0x11, 0x1F, 0x11, 0x11}, // A
	{0x1E, 0x11, 0x11, 0x1E, 0x11, 0x11, 0x1E}, // B
	{0x0E, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0E}, // C
	{0x1E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x1E}, // D
	{0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x1F}, // E
	{0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x10}, // F
	{0x0E, 0x11, 0x10, 0x17, 0x11, 0x11, 0x0F}, // G
	{0x11, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11}, // H
	{0x0E, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E}, // I
	{0x07, 0x02, 0x02, 0x02, 0x02, 0x12, 0x0C}, // J
	{0x11, 0x12, 0x14, 0x18, 0x14, 0x12, 0x11}, // K
	{0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1F}, // L
	{0x11, 0x1B, 0x15, 0x15, 0x11, 0x11, 0x11}, // M
	{0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11}, // N
	{0x0E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E}, // O
	{0x1E, 0x11, 0x11, 0x1E, 0x10, 0x10, 0x10}, // P
	{0x0E, 0x11, 0x11, 0x11, 0x15, 0x12, 0x0D}, // Q
	{0x1E, 0x11, 0x11, 0x1E, 0x14, 0x12, 0x11}, // R
	{0x0E, 0x11, 0x10, 0x0E, 0x01, 0x11, 0x0E}, // S
	{0x1F, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04}, // T
	{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E}, // U
	{0x11, 0x11, 0x11, 0x11, 0x0A, 0x0A, 0x04}, // V
	{0x11, 0x11, 0x11, 0x15, 0x15, 0x1B, 0x11}, // W
	{0x11, 0x11, 0x0A, 0x04, 0x0A, 0x11, 0x11}, // X
	{0x11, 0x11, 0x0A, 0x04, 0x04, 0x04, 0x04}, // Y
	{0x1F, 0x01, 0x02, 0x04, 0x08, 0x10, 0x1F}, // Z
}

var lowercaseGlyphs = [26][7]byte{
	{0x00, 0x00, 0x0E, 0x01, 0x0F, 0x11, 0x0F}, // a
	{0x10, 0x10, 0x1E, 0x11, 0x11, 0x11, 0x1E}, // b
	{0x00, 0x00, 0x0E, 0x11, 0x10, 0x11, 0x0E}, // c
	{0x01, 0x01, 0x0F, 0x11, 0x11, 0x11, 0x0F}, // d
	{0x00, 0x00, 0x0E, 0x11, 0x1F, 0x10, 0x0E}, // e
	{0x06, 0x09, 0x08, 0x1C, 0x08, 0x08, 0x08}, // f
	{0x00, 0x00, 0x0F, 0x11, 0x0F, 0x01, 0x0E}, // g
	{0x10, 0x10, 0x16, 0x19, 0x11, 0x11, 0x11}, // h
	{0x04, 0x00, 0x0C, 0x04, 0x04, 0x04, 0x0E}, // i
	{0x02, 0x00, 0x06, 0x02, 0x02, 0x12, 0x0C}, // j
	{0x10, 0x10, 0x12, 0x14, 0x18, 0x14, 0x12}, // k
	{0x0C, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E}, // l
	{0x00, 0x00, 0x1A, 0x15, 0x15, 0x11, 0x11}, // m
	{0x00, 0x00, 0x16, 0x19, 0x11, 0x11, 0x11}, // n
	{0x00, 0x00, 0x0E, 0x11, 0x11, 0x11, 0x0E}, // o
	{0x00, 0x00, 0x1E, 0x11, 0x1E, 0x10, 0x10}, // p
	{0x00, 0x00, 0x0F, 0x11, 0x0F, 0x01, 0x01}, // q
	{0x00, 0x00, 0x16, 0x19, 0x10, 0x10, 0x10}, // r
	{0x00, 0x00, 0x0F, 0x10, 0x0E, 0x01, 0x1E}, // s
	{0x08, 0x08, 0x1C, 0x08, 0x08, 0x09, 0x06}, // t
	{0x00, 0x00, 0x11, 0x11, 0x11, 0x13, 0x0D}, // u
	{0x00, 0x00, 0x11, 0x11, 0x11, 0x0A, 0x04}, // v
	{0x00, 0x00, 0x11, 0x11, 0x15, 0x15, 0x0A}, // w
	{0x00, 0x00, 0x11, 0x0A, 0x04, 0x0A, 0x11}, // x
	{0x00, 0x00, 0x11, 0x11, 0x0F, 0x01, 0x0E}, // y
	{0x00, 0x00, 0x1F, 0x02, 0x04, 0x08, 0x1F}, // z
}

var digitGlyphs = [10][7]byte{
	{0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E}, // 0
	{0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E}, // 1
	{0x0E, 0x11, 0x01, 0x02, 0x04, 0x08, 0x1F}, // 2
	{0x0E, 0x11, 0x01, 0x06, 0x01, 0x11, 0x0E}, // 3
	{0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02}, // 4
	{0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E}, // 5
	{0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E}, // 6
	{0x1F, 0x01, 0x02, 0x04, 0x04, 0x04, 0x04}, // 7
	{0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E}, // 8
	{0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C}, // 9
}

func parseTimeOrNow(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t
}
