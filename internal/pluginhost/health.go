package pluginhost

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func WaitHealthy(ctx context.Context, addr, path string, interval time.Duration) error {
	url := "http://" + addr + path
	client := &http.Client{Timeout: 2 * time.Second}
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
