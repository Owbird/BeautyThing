package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"beautything/internal/render"
	"beautything/internal/syncthing"
	"beautything/internal/tui"
)

func main() {
	cfg, err := parseConfig()
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := syncthing.NewClient(syncthing.ClientConfig{
		BaseURL:         cfg.baseURL,
		APIKey:          cfg.apiKey,
		Timeout:         cfg.timeout,
		InsecureSkipTLS: cfg.insecure,
		DiskOnly:        cfg.diskOnly,
		EventFilter:     cfg.eventFilter,
	})

	streamer := syncthing.NewStreamer(client)

	if cfg.since < 0 {
		latestID, err := streamer.LatestEventID(ctx)
		if err != nil {
			log.Fatalf("resolve latest event id: %v", err)
		}
		cfg.since = latestID
	}

	eventCh := make(chan tui.StreamEvent, 64)
	errCh := make(chan error, 1)

	go func() {
		defer close(eventCh)
		defer close(errCh)

		err := streamer.Stream(ctx, cfg.since, func(event syncthing.Event, gap bool) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case eventCh <- tui.StreamEvent{
				Entry: render.FormatEvent(event),
				At:    event.Time,
				Gap:   gap,
			}:
				return nil
			}
		})
		if err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()

	program := tui.NewProgram(ctx, tui.Config{
		SourceURL: cfg.baseURL,
		DiskOnly:  cfg.diskOnly,
		NoColor:   cfg.noColor,
		StartID:   cfg.since,
		MaxEvents: 500,
	}, eventCh, errCh)

	if _, err := program.Run(); err != nil {
		log.Fatal(err)
	}
}

type config struct {
	baseURL     string
	apiKey      string
	timeout     time.Duration
	insecure    bool
	diskOnly    bool
	noColor     bool
	since       int64
	eventFilter []string
}

func parseConfig() (config, error) {
	var cfg config

	defaultURL := envOr("SYNCTHING_URL", "http://127.0.0.1:8384")
	defaultAPIKey := os.Getenv("SYNCTHING_API_KEY")

	var events string

	flag.StringVar(&cfg.baseURL, "url", defaultURL, "Syncthing base URL (or use SYNCTHING_URL)")
	flag.StringVar(&cfg.apiKey, "api-key", defaultAPIKey, "Syncthing API key (or use SYNCTHING_API_KEY)")
	flag.DurationVar(&cfg.timeout, "timeout", 55*time.Second, "long-poll timeout per request")
	flag.BoolVar(&cfg.insecure, "insecure", false, "skip TLS verification for HTTPS Syncthing endpoints")
	flag.BoolVar(&cfg.diskOnly, "disk-only", false, "use /rest/events/disk for LocalChangeDetected and RemoteChangeDetected only")
	flag.BoolVar(&cfg.noColor, "no-color", false, "disable ANSI colors")
	flag.Int64Var(&cfg.since, "since", -1, "start after this event ID (default -1 means resume from latest)")
	flag.StringVar(&events, "events", "", "comma-separated event type filter for /rest/events")
	flag.Parse()

	if cfg.apiKey == "" {
		return config{}, fmt.Errorf("missing Syncthing API key: pass -api-key or set SYNCTHING_API_KEY")
	}

	cfg.eventFilter = splitCSV(events)
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return filtered
}
