package main

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

// SettingsManager handles loading and saving user settings
type SettingsManager struct {
	path     string
	settings UserSettings
}

// NewSettingsManager creates a settings manager with the default config path
func NewSettingsManager() (*SettingsManager, error) {
	path, err := getConfigPath()
	if err != nil {
		return nil, err
	}
	return &SettingsManager{path: path}, nil
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

// DefaultSettings returns the default settings
func DefaultSettings() UserSettings {
	return UserSettings{
		Quality:         DefaultQualityIndex(),
		FPS:             DefaultFPSIndex(),
		Codec:           DefaultCodecIndex(),
		AdaptiveBitrate: false,
		QualityMode:     false,
	}
}

// Load reads settings from the config file.
// Returns default settings if file doesn't exist or is invalid.
func (sm *SettingsManager) Load() (UserSettings, error) {
	sm.settings = DefaultSettings()

	data, err := os.ReadFile(sm.path)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist - use defaults, not an error
			return sm.settings, nil
		}
		return sm.settings, err
	}

	// Parse JSON, keeping defaults for missing fields
	if err := json.Unmarshal(data, &sm.settings); err != nil {
		// Invalid JSON - use defaults
		return DefaultSettings(), nil
	}

	// Validate indices are in bounds
	sm.validateSettings()

	return sm.settings, nil
}

// validateSettings ensures loaded settings are within valid ranges
func (sm *SettingsManager) validateSettings() {
	if sm.settings.Quality < 0 || sm.settings.Quality >= len(QualityPresets) {
		sm.settings.Quality = DefaultQualityIndex()
	}
	if sm.settings.FPS < 0 || sm.settings.FPS >= len(FPSPresets) {
		sm.settings.FPS = DefaultFPSIndex()
	}
	// Codec validation must be done after InitAvailableCodecs()
	// and is handled separately in initialModel()
}

// Save writes current settings to the config file
func (sm *SettingsManager) Save(settings UserSettings) error {
	sm.settings = settings

	// Ensure directory exists
	dir := filepath.Dir(sm.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Marshal with indentation for readability
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(sm.path, data, 0644)
}

// GetSettings returns the current settings
func (sm *SettingsManager) GetSettings() UserSettings {
	return sm.settings
}
