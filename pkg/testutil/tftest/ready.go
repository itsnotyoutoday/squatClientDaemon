package tftest

import (
	"context"
	"time"

	"github.com/itsnotyoutoday/squatClientDaemon/internal/tfbridge"
)

func waitReady(b *tfbridge.Bridge) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return b.WaitReady(ctx)
}
