// Package sunshine talks to Sunshine's unauthenticated serverinfo endpoint.
package sunshine

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const timeout = 5 * time.Second

// Client queries a Sunshine host. BaseURL is "http://<host>:47989".
// A nil HTTP uses a client with a 5 s timeout.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

type serverInfo struct {
	XMLName     xml.Name `xml:"root"`
	CurrentGame string   `xml:"currentgame"`
}

// CurrentGame returns the running app id, 0 meaning no app. A missing,
// empty or unparsable value is an error (any non-zero integer, negatives included, means an app runs), never 0.
func (c *Client) CurrentGame(ctx context.Context) (int, error) {
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/serverinfo", nil)
	if err != nil {
		return 0, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("serverinfo: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	var si serverInfo
	if err := xml.Unmarshal(body, &si); err != nil {
		return 0, fmt.Errorf("serverinfo: %w", err)
	}
	s := strings.TrimSpace(si.CurrentGame)
	if s == "" {
		return 0, fmt.Errorf("serverinfo: missing currentgame")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("serverinfo: bad currentgame %q", s)
	}
	return n, nil
}
