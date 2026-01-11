package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// copyToClipboard copies text to the macOS clipboard using pbcopy
func copyToClipboard(text string) error {
	cmd := exec.Command("pbcopy")
	pipe, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := pipe.Write([]byte(text)); err != nil {
		return err
	}
	if err := pipe.Close(); err != nil {
		return err
	}
	return cmd.Wait()
}

// normalizeSignalURL converts HTTP URLs to WebSocket URLs
func normalizeSignalURL(url string) string {
	if strings.HasPrefix(url, "http://") {
		return "ws://" + strings.TrimPrefix(url, "http://")
	} else if strings.HasPrefix(url, "https://") {
		return "wss://" + strings.TrimPrefix(url, "https://")
	} else if !strings.HasPrefix(url, "ws://") && !strings.HasPrefix(url, "wss://") {
		return "wss://" + url
	}
	return url
}

// requestRoomCodeFromServer requests a unique room code from the signal server
func requestRoomCodeFromServer(signalURL string) tea.Cmd {
	return func() tea.Msg {
		// Convert WebSocket URL to HTTP URL for the API call
		apiURL := signalURL
		apiURL = strings.Replace(apiURL, "wss://", "https://", 1)
		apiURL = strings.Replace(apiURL, "ws://", "http://", 1)
		apiURL = strings.TrimSuffix(apiURL, "/") + "/api/reserve"

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Post(apiURL, "application/json", nil)
		if err != nil {
			return roomCodeReceivedMsg{err: fmt.Errorf("failed to request room code: %w", err)}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return roomCodeReceivedMsg{err: fmt.Errorf("server returned status %d", resp.StatusCode)}
		}

		var result struct {
			Room   string `json:"room"`
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return roomCodeReceivedMsg{err: fmt.Errorf("failed to decode response: %w", err)}
		}

		return roomCodeReceivedMsg{roomCode: result.Room, roomSecret: result.Secret}
	}
}

// truncate shortens a string to the given max length with ellipsis
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// max returns the larger of two integers
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// formatDuration formats a duration for display (e.g., "1h 23m 45s")
func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// formatNumber formats an integer with commas (e.g., "1,234,567")
func formatNumber(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}

	var result strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result.WriteRune(',')
		}
		result.WriteRune(c)
	}
	return result.String()
}

// formatBytes formats bytes to human-readable format (e.g., "1.5 MB")
func formatBytes(b uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
