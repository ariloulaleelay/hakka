package gateways

import "context"

// Gateway is a transport that accepts external input and pushes it into
// the Engine. Implementations should be safe to Start once and Stop once.
type Gateway interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
