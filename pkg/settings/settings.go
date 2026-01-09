package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// UserSettings holds persistable user preferences
type UserSettings struct {
	Quality         int  `json:"quality"`
	FPS             int  `json:"fps"`
	Codec           int  `json:"codec"`
	AdaptiveBitrate bool `json:"adaptiveBitrate"`
	QualityMode     bool `json:"qualityMode"`
}

// DefaultSettings returns the default settings
func DefaultSettings() UserSettings {
	return UserSettings{
		Quality:         3, // Very High (index into QualityPresets)
		FPS:             1, // 30 fps (index into FPSPresets)
		Codec:           0, // VP8 (index into AvailableCodecs)
		AdaptiveBitrate: false,
		QualityMode:     false,
	}
}

// getConfigPath returns the config file path.
// Uses XDG_CONFIG_HOME if set, otherwise ~/Library/Application Support/gopeep/
func getConfigPath() (string, error) {
	var configDir string

	// Check for XDG override (for power users)
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		configDir = filepath.Join(xdg, "gopeep")
	} else {
		// Use macOS standard location
		userConfigDir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(userConfigDir, "gopeep")
	}

	return filepath.Join(configDir, "config.json"), nil
}

// Load reads settings from the config file.
// Returns default settings if file doesn't exist or is invalid.
func Load() (UserSettings, error) {
	settings := DefaultSettings()

	path, err := getConfigPath()
	if err != nil {
		return settings, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist - use defaults, not an error
			return settings, nil
		}
		return settings, err
	}

	// Parse JSON, keeping defaults for missing fields
	if err := json.Unmarshal(data, &settings); err != nil {
		// Invalid JSON - use defaults
		return DefaultSettings(), nil
	}

	return settings, nil
}

// Save writes settings to the config file
func Save(settings UserSettings) error {
	path, err := getConfigPath()
	if err != nil {
		return err
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Marshal with indentation for readability
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
