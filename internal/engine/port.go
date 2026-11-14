package engine

import (
	"context"
	"fmt"
	"net"
	"time"
)

const (
	portAttempts   = 5
	portRetryDelay = 50 * time.Millisecond
)

// FreePort asks the OS for a free loopback TCP port and then releases it for
// rclone to bind. It retries a few times because another process can claim the
// port in the short window between Listen and rclone's bind.
func FreePort(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := 0; attempt < portAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			lastErr = err
		} else {
			addr, ok := listener.Addr().(*net.TCPAddr)
			closeErr := listener.Close()
			if ok && closeErr == nil && addr.Port > 0 && addr.Port <= 65535 {
				return addr.Port, nil
			}
			if closeErr != nil {
				lastErr = closeErr
			} else {
				lastErr = fmt.Errorf("engine: unexpected listener address %s", listener.Addr())
			}
		}
		if attempt == portAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(portRetryDelay):
		}
	}
	return 0, fmt.Errorf("engine: could not acquire a loopback port: %w", lastErr)
}
