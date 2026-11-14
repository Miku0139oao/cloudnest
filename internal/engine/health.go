package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloudnest/internal/rc"
)

const (
	healthAttempts = 40
	healthInterval = 100 * time.Millisecond
	healthTimeout  = 1500 * time.Millisecond
)

// WaitReady polls POST core/version with Basic auth until rclone answers or
// the 40 attempts (100ms apart) are exhausted. Each request has a 1.5 second
// timeout. Authentication failures abort immediately because retrying cannot
// fix bad credentials.
func WaitReady(ctx context.Context, client *rc.Client) error {
	if client == nil {
		return errors.New("engine: WaitReady: nil client")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var lastErr error
	for attempt := 0; attempt < healthAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		var version rc.Version
		err := client.Call(ctx, "core/version", nil, &version, rc.WithTimeout(healthTimeout))
		if err == nil {
			return nil
		}
		if rc.IsAuth(err) {
			return fmt.Errorf("engine: rclone rejected the RC credentials: %w", err)
		}
		lastErr = err

		if attempt == healthAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthInterval):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no response")
	}
	return fmt.Errorf("engine: rclone did not become ready: %w", lastErr)
}
