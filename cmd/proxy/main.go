package main

import v1internal "antigravity-go-proxy/gen/v1internal"

func main() {
	// Keep both recovered service constructors in the Phase 0 compile gate.
	// The agy descriptor places content generation on PredictionService and
	// provisioning/chat operations on CloudCode.
	_ = v1internal.NewCloudCodeClient
	_ = v1internal.NewPredictionServiceClient
}
