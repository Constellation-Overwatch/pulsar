//go:build !detection

package video

import (
	"context"

	"github.com/Constellation-Overwatch/pulsar/pkg/shared"
)

// startDeviceCaptures is a no-op without the detection build tag.
// Device capture requires GoCV (build with: go build -tags detection).
func startDeviceCaptures(_ context.Context, _ []shared.EntityState, _ *RTSPServer) func() {
	return func() {}
}
