package main

import (
	"strconv"
	"strings"
)

// FPSPreset defines a framerate preset
type FPSPreset struct {
	Value       int
	Name        string
	Description string
}

// FPS presets from lowest to highest
var FPSPresets = []FPSPreset{
	{Value: 15, Name: "15", Description: "low power"},
	{Value: 24, Name: "24", Description: "cinematic"},
	{Value: 30, Name: "30", Description: "standard"},
	{Value: 60, Name: "60", Description: "smooth"},
	{Value: 120, Name: "120", Description: "ultra smooth"},
}

// DefaultFPSIndex returns the index of the default FPS preset (30)
func DefaultFPSIndex() int {
	return 2 // 30 fps
}

// FPSByValue finds an FPS preset by value
func FPSByValue(value int) *FPSPreset {
	for i := range FPSPresets {
		if FPSPresets[i].Value == value {
			return &FPSPresets[i]
		}
	}
	return nil
}

// ParseFPSFlag parses the --fps flag value
func ParseFPSFlag(value string) int {
	value = strings.TrimSpace(value)

	// Try to parse as integer
	if fps, err := strconv.Atoi(value); err == nil && fps > 0 {
		return fps
	}

	// Default to 30
	return FPSPresets[DefaultFPSIndex()].Value
}

// FPSIndexForValue returns the index of the preset matching the given FPS value,
// or the default index if not found
func FPSIndexForValue(fps int) int {
	for i, preset := range FPSPresets {
		if preset.Value == fps {
			return i
		}
	}
	return DefaultFPSIndex()
}
