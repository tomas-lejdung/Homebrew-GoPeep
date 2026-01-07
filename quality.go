package main

import "strings"

// QualityPreset defines a video quality preset
type QualityPreset struct {
	Name        string
	Bitrate     int    // in kbps
	Description string // short description for UI
}

// Quality presets from lowest to highest
var QualityPresets = []QualityPreset{
	{Name: "Low", Bitrate: 500, Description: "500 kbps"},
	{Name: "Medium", Bitrate: 1500, Description: "1.5 Mbps"},
	{Name: "High", Bitrate: 3000, Description: "3 Mbps"},
	{Name: "Ultra", Bitrate: 6000, Description: "6 Mbps"},
	{Name: "Extreme", Bitrate: 10000, Description: "10 Mbps"},
	{Name: "Insane", Bitrate: 15000, Description: "15 Mbps"},
	{Name: "Max", Bitrate: 20000, Description: "20 Mbps"},
}

// DefaultQualityIndex returns the index of the default quality preset (Medium)
func DefaultQualityIndex() int {
	return 1 // Medium
}

// QualityByName finds a quality preset by name (case-insensitive)
func QualityByName(name string) *QualityPreset {
	name = strings.ToLower(name)
	for i := range QualityPresets {
		if strings.ToLower(QualityPresets[i].Name) == name {
			return &QualityPresets[i]
		}
	}
	return nil
}

// QualityNameToBitrate converts a quality name to bitrate
// Returns default (Medium) bitrate if name not found
func QualityNameToBitrate(name string) int {
	preset := QualityByName(name)
	if preset != nil {
		return preset.Bitrate
	}
	return QualityPresets[DefaultQualityIndex()].Bitrate
}

// ParseQualityFlag parses the --quality flag value and returns bitrate
// Supports both preset names (low, med, hi) and the new names
func ParseQualityFlag(value string) int {
	value = strings.ToLower(value)

	// Handle legacy short names
	switch value {
	case "lo", "low":
		return QualityPresets[0].Bitrate
	case "med", "medium":
		return QualityPresets[1].Bitrate
	case "hi", "high":
		return QualityPresets[2].Bitrate
	case "ultra":
		return QualityPresets[3].Bitrate
	case "extreme":
		return QualityPresets[4].Bitrate
	case "insane":
		return QualityPresets[5].Bitrate
	case "max":
		return QualityPresets[6].Bitrate
	}

	// Try exact match
	if preset := QualityByName(value); preset != nil {
		return preset.Bitrate
	}

	// Default to medium
	return QualityPresets[DefaultQualityIndex()].Bitrate
}
