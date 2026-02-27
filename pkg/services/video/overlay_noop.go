//go:build !detection

package video

import "github.com/Constellation-Overwatch/pulsar/pkg/services/logger"

// OverlayWriter is a no-op when built without the "detection" build tag.
type OverlayWriter struct{}

// NewOverlayWriter returns nil when detection is not compiled.
func NewOverlayWriter(_, _ string, _ int) (*OverlayWriter, error) {
	logger.Info("[overlay] not compiled (build with: go build -tags detection)")
	return nil, nil
}

// Close is a no-op.
func (w *OverlayWriter) Close() {}
