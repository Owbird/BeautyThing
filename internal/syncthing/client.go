package syncthing

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Event is the subset of a Syncthing REST event used by BeautyThing.
type Event struct {
	ID       int64           `json:"id"`
	GlobalID int64           `json:"globalID"`
	Type     string          `json:"type"`
	Time     time.Time       `json:"time"`
	Data     json.RawMessage `json:"data"`
}

// ClientConfig configures REST access to a Syncthing instance.
type ClientConfig struct {
	BaseURL         string
	APIKey          string
	Timeout         time.Duration
	InsecureSkipTLS bool
	DiskOnly        bool
	EventFilter     []string
}

// Client fetches events from the Syncthing REST API.
type Client struct {
	baseURL     *url.URL
	apiKey      string
	diskOnly    bool
	eventFilter []string
	httpClient  *http.Client
}

// NewClient constructs a Syncthing API client from the provided config.
func NewClient(cfg ClientConfig) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipTLS,
	}

	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		panic(fmt.Sprintf("invalid Syncthing base URL %q: %v", cfg.BaseURL, err))
	}

	return &Client{
		baseURL:     baseURL,
		apiKey:      cfg.APIKey,
		diskOnly:    cfg.DiskOnly,
		eventFilter: append([]string(nil), cfg.EventFilter...),
		httpClient: &http.Client{
			Timeout:   cfg.Timeout + 5*time.Second,
			Transport: transport,
		},
	}
}

func (c *Client) FetchEvents(ctx context.Context, since int64, timeout time.Duration, limit int) ([]Event, error) {
	endpoint := "/rest/events"
	if c.diskOnly {
		endpoint = "/rest/events/disk"
	}

	reqURL := *c.baseURL
	reqURL.Path = path.Join(c.baseURL.Path, endpoint)

	query := reqURL.Query()
	query.Set("since", strconv.FormatInt(since, 10))
	query.Set("timeout", strconv.Itoa(int(timeout.Seconds())))
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if !c.diskOnly && len(c.eventFilter) > 0 {
		query.Set("events", strings.Join(c.eventFilter, ","))
	}
	reqURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request events: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("syncthing returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var events []Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return events, nil
}

// Streamer long-polls Syncthing and emits events in ID order.
type Streamer struct {
	client *Client
}

// NewStreamer returns a Streamer that uses the provided Client.
func NewStreamer(client *Client) *Streamer {
	return &Streamer{client: client}
}

// LatestEventID fetches the latest known event ID from Syncthing.
func (s *Streamer) LatestEventID(ctx context.Context) (int64, error) {
	events, err := s.client.FetchEvents(ctx, 0, 1*time.Second, 1)
	if err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return 0, nil
	}
	return events[len(events)-1].ID, nil
}

// Stream continuously polls for events after since and passes them to handle.
func (s *Streamer) Stream(ctx context.Context, since int64, handle func(event Event, gap bool) error) error {
	backoff := time.Second

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		events, err := s.client.FetchEvents(ctx, since, 55*time.Second, 0)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			if backoff < 10*time.Second {
				backoff *= 2
			}
			continue
		}

		backoff = time.Second
		for _, event := range events {
			gap := since != 0 && event.ID > since+1
			if err := handle(event, gap); err != nil {
				return err
			}
			since = event.ID
		}
	}
}
