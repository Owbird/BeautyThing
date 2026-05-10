package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"beautything/internal/syncthing"
)

const (
	reset  = "\033[0m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	cyan   = "\033[36m"
)

// Entry is a human-readable representation of a Syncthing event.
type Entry struct {
	Time    string
	Type    string
	ID      int64
	Summary string
	Tone    Tone
}

// Tone describes the visual severity or intent of an event entry.
type Tone string

const (
	// ToneNeutral marks routine activity without a strong status signal.
	ToneNeutral Tone = "neutral"
	// ToneInfo marks progress and active work.
	ToneInfo Tone = "info"
	// ToneSuccess marks successful completion events.
	ToneSuccess Tone = "success"
	// ToneWarn marks degraded or unusual events that are not fatal.
	ToneWarn Tone = "warn"
	// ToneError marks failed or disconnected events.
	ToneError Tone = "error"
)

// Printer writes formatted event entries to an io.Writer.
type Printer struct {
	out   io.Writer
	color bool
}

// NewPrinter returns a Printer that emits line-oriented event output.
func NewPrinter(out io.Writer, color bool) *Printer {
	return &Printer{out: out, color: color}
}

// Print renders a Syncthing event as a single human-readable line.
func (p *Printer) Print(event syncthing.Event, gap bool) error {
	if gap {
		if _, err := fmt.Fprintf(p.out, "%s[warn]%s missed one or more events before id=%d\n", p.paint(yellow), p.paint(reset), event.ID); err != nil {
			return err
		}
	}

	entry := FormatEvent(event)
	line := fmt.Sprintf(
		"%s  %s%-22s%s  %s#%d%s  %s",
		entry.Time,
		p.paint(colorForTone(entry.Tone)),
		entry.Type,
		p.paint(reset),
		p.paint(dim),
		entry.ID,
		p.paint(reset),
		entry.Summary,
	)

	_, err := fmt.Fprintln(p.out, line)
	return err
}

// FormatEvent converts a raw Syncthing event into a summarized Entry.
func FormatEvent(event syncthing.Event) Entry {
	entry := Entry{
		Time: event.Time.Local().Format("15:04:05"),
		Type: event.Type,
		ID:   event.ID,
		Tone: toneForType(event.Type),
	}

	entry.Summary = summarizeEvent(event)
	return entry
}

func (p *Printer) paint(code string) string {
	if !p.color {
		return ""
	}
	return code
}

func colorForTone(tone Tone) string {
	switch tone {
	case ToneError:
		return red
	case ToneSuccess:
		return green
	case ToneInfo:
		return blue
	case ToneWarn:
		return yellow
	default:
		return cyan
	}
}

func toneForType(eventType string) Tone {
	switch {
	case strings.Contains(eventType, "Failure"), strings.Contains(eventType, "Rejected"), strings.Contains(eventType, "Disconnected"):
		return ToneError
	case strings.Contains(eventType, "Connected"), strings.Contains(eventType, "Finished"), strings.Contains(eventType, "Complete"):
		return ToneSuccess
	case strings.Contains(eventType, "Started"), strings.Contains(eventType, "Scan"), strings.Contains(eventType, "Progress"):
		return ToneInfo
	case strings.Contains(eventType, "Change"), strings.Contains(eventType, "Index"):
		return ToneNeutral
	default:
		return ToneWarn
	}
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}

	return string(encoded)
}

func summarizeEvent(event syncthing.Event) string {
	switch event.Type {
	case "LocalIndexUpdated":
		var payload struct {
			Folder    string   `json:"folder"`
			Items     int      `json:"items"`
			Filenames []string `json:"filenames"`
			Sequence  int64    `json:"sequence"`
		}
		if decodeData(event.Data, &payload) {
			parts := []string{
				"folder " + payload.Folder,
				plural(int64(payload.Items), "item"),
			}
			if payload.Sequence > 0 {
				parts = append(parts, "seq "+strconv.FormatInt(payload.Sequence, 10))
			}
			if len(payload.Filenames) > 0 {
				parts = append(parts, "changed "+joinPreview(payload.Filenames, 2))
			}
			return strings.Join(parts, "  ")
		}
	case "StateChanged":
		var payload struct {
			Folder   string  `json:"folder"`
			From     string  `json:"from"`
			To       string  `json:"to"`
			Duration float64 `json:"duration"`
		}
		if decodeData(event.Data, &payload) {
			parts := []string{}
			if payload.Folder != "" {
				parts = append(parts, "folder "+payload.Folder)
			}
			parts = append(parts, stateLabel(payload.From)+" -> "+stateLabel(payload.To))
			if payload.Duration > 0 {
				parts = append(parts, "after "+formatSeconds(payload.Duration))
			}
			return strings.Join(parts, "  ")
		}
	case "ItemStarted":
		var payload struct {
			Action string `json:"action"`
			Folder string `json:"folder"`
			Item   string `json:"item"`
			Type   string `json:"type"`
		}
		if decodeData(event.Data, &payload) {
			parts := []string{}
			if payload.Folder != "" {
				parts = append(parts, "folder "+payload.Folder)
			}
			parts = append(parts, formatAction(payload.Action, payload.Type))
			if payload.Item != "" {
				parts = append(parts, shortPath(payload.Item))
			}
			return strings.Join(parts, "  ")
		}
	case "ItemFinished":
		var payload struct {
			Action string `json:"action"`
			Folder string `json:"folder"`
			Item   string `json:"item"`
			Type   string `json:"type"`
			Error  any    `json:"error"`
		}
		if decodeData(event.Data, &payload) {
			parts := []string{}
			if payload.Folder != "" {
				parts = append(parts, "folder "+payload.Folder)
			}
			status := "done"
			if payload.Error != nil {
				status = "failed"
			}
			parts = append(parts, status, formatAction(payload.Action, payload.Type))
			if payload.Item != "" {
				parts = append(parts, shortPath(payload.Item))
			}
			if payload.Error != nil {
				parts = append(parts, fmt.Sprintf("error %v", payload.Error))
			}
			return strings.Join(parts, "  ")
		}
	case "FolderSummary":
		var payload struct {
			Folder  string `json:"folder"`
			Summary struct {
				State          string `json:"state"`
				NeedBytes      int64  `json:"needBytes"`
				NeedTotalItems int64  `json:"needTotalItems"`
				Errors         int64  `json:"errors"`
				PullErrors     int64  `json:"pullErrors"`
			} `json:"summary"`
		}
		if decodeData(event.Data, &payload) {
			parts := []string{
				"folder " + payload.Folder,
				"state " + payload.Summary.State,
			}
			if payload.Summary.NeedTotalItems > 0 || payload.Summary.NeedBytes > 0 {
				parts = append(parts,
					"needs "+plural(payload.Summary.NeedTotalItems, "item"),
					"("+humanBytes(payload.Summary.NeedBytes)+")",
				)
			} else {
				parts = append(parts, "in sync")
			}
			if payload.Summary.Errors > 0 || payload.Summary.PullErrors > 0 {
				parts = append(parts, fmt.Sprintf("errors %d/%d", payload.Summary.Errors, payload.Summary.PullErrors))
			}
			return strings.Join(parts, "  ")
		}
	case "FolderCompletion":
		var payload struct {
			Folder      string  `json:"folder"`
			Device      string  `json:"device"`
			Completion  float64 `json:"completion"`
			NeedBytes   int64   `json:"needBytes"`
			NeedItems   int64   `json:"needItems"`
			NeedDeletes int64   `json:"needDeletes"`
			RemoteState string  `json:"remoteState"`
		}
		if decodeData(event.Data, &payload) {
			parts := []string{
				"folder " + payload.Folder,
				"device " + shortID(payload.Device),
				fmt.Sprintf("%.1f%%", payload.Completion),
			}
			if payload.NeedItems > 0 || payload.NeedBytes > 0 || payload.NeedDeletes > 0 {
				parts = append(parts,
					"remaining "+plural(payload.NeedItems, "item"),
					humanBytes(payload.NeedBytes),
				)
				if payload.NeedDeletes > 0 {
					parts = append(parts, plural(payload.NeedDeletes, "delete"))
				}
			} else {
				parts = append(parts, "fully synced")
			}
			if payload.RemoteState != "" {
				parts = append(parts, "remote "+payload.RemoteState)
			}
			return strings.Join(parts, "  ")
		}
	}

	return compactJSON(event.Data)
}

func decodeData(raw json.RawMessage, target any) bool {
	return json.Unmarshal(raw, target) == nil
}

func plural(n int64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.FormatInt(n, 10) + " " + noun + "s"
}

func joinPreview(items []string, limit int) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) <= limit {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:limit], ", ") + fmt.Sprintf(" +%d more", len(items)-limit)
}

func shortID(deviceID string) string {
	if len(deviceID) <= 7 {
		return deviceID
	}
	return deviceID[:7]
}

func formatSeconds(seconds float64) string {
	if seconds < 1 {
		return fmt.Sprintf("%.0fms", seconds*1000)
	}
	return fmt.Sprintf("%.2fs", seconds)
}

func stateLabel(state string) string {
	switch state {
	case "sync-preparing":
		return "preparing"
	case "syncing":
		return "syncing"
	case "idle":
		return "idle"
	case "scanning":
		return "scanning"
	default:
		return state
	}
}

func formatAction(action, itemType string) string {
	if itemType == "" {
		return action
	}
	return action + " " + itemType
}

func shortPath(path string) string {
	const keep = 2

	parts := strings.Split(path, "/")
	if len(parts) <= keep {
		return path
	}
	return ".../" + strings.Join(parts[len(parts)-keep:], "/")
}

func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}

	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(n)
	unit := "B"
	for _, next := range units {
		value /= 1024
		unit = next
		if value < 1024 {
			break
		}
	}

	if value >= 100 {
		return fmt.Sprintf("%.0f %s", value, unit)
	}
	if value >= 10 {
		return fmt.Sprintf("%.1f %s", value, unit)
	}
	return fmt.Sprintf("%.2f %s", value, unit)
}

// SinceLabel returns a compact relative time label for display in the TUI.
func SinceLabel(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}

	delta := now.Sub(t).Round(time.Second)
	if delta < time.Second {
		return "just now"
	}
	return delta.String() + " ago"
}
