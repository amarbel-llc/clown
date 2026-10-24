package pluginhost

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// WaitHealthy polls http://<addr><path> until it answers 200 or ctx ends.
func WaitHealthy(ctx context.Context, addr, path string, interval time.Duration) error {
	return WaitHealthyHandshake(ctx, Handshake{NetworkType: "tcp", Address: addr}, path, interval)
}

// WaitHealthyHandshake is WaitHealthy for the server hs announced, over its
// own transport (a unix socket for a unix handshake).
func WaitHealthyHandshake(ctx context.Context, hs Handshake, path string, interval time.Duration) error {
	url := hs.BaseURL() + path
	client := *hs.Client()
	client.Timeout = 2 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("healthcheck: creating request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("healthcheck: %s: %w", url, ctx.Err())
		case <-ticker.C:
		}
	}
}
