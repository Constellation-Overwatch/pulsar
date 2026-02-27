//go:build !detection

package detector

import (
	"context"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"
	"github.com/Constellation-Overwatch/pulsar/pkg/services/publisher"
	"github.com/Constellation-Overwatch/pulsar/pkg/services/video"
	"github.com/Constellation-Overwatch/pulsar/pkg/shared"
)

// StartDetector is a no-op when built without the "detection" build tag.
// Build with: go build -tags detection
func StartDetector(ctx context.Context, entities []shared.EntityState, orgID string, pub *publisher.OverwatchPublisher, modelPath, onnxLibPath string, overlayWriters map[string]*video.OverlayWriter) {
	logger.Info("[detector] CV detection not compiled (build with: go build -tags detection)")
}
