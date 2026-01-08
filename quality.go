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
	{Name: "Very High", Bitrate: 6000, Description: "6 Mbps"},
	{Name: "Ultra", Bitrate: 10000, Description: "10 Mbps"},
	{Name: "Extreme", Bitrate: 15000, Description: "15 Mbps"},
	{Name: "Max", Bitrate: 20000, Description: "20 Mbps"},
	{Name: "Insane", Bitrate: 50000, Description: "50 Mbps"},
}

// DefaultQualityIndex returns the index of the default quality preset (Very High)
func DefaultQualityIndex() int {
	return 3 // Very High (6 Mbps)
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

// QualityModeParams returns encoder parameters for quality mode based on bitrate
// Returns: cqLevel (VP8/VP9, lower=better), crf (x264, lower=better), vtQuality (VideoToolbox, higher=better)
func QualityModeParams(bitrate int) (cqLevel int, crf float32, vtQuality float32) {
	switch bitrate {
	case 500:
		return 40, 28, 0.50
	case 1500:
		return 32, 24, 0.65
	case 3000:
		return 24, 21, 0.75
	case 6000:
		return 18, 18, 0.85
	case 10000:
		return 12, 16, 0.90
	case 15000:
		return 8, 14, 0.95
	case 20000:
		return 4, 12, 0.98
	case 50000:
		return 2, 10, 1.00
	default:
		// Interpolate for custom bitrates
		if bitrate < 500 {
			return 45, 30, 0.40
		} else if bitrate < 1500 {
			return 36, 26, 0.55
		} else if bitrate < 3000 {
			return 28, 22, 0.70
		} else if bitrate < 6000 {
			return 20, 19, 0.80
		} else if bitrate < 10000 {
			return 15, 17, 0.87
		} else if bitrate < 15000 {
			return 10, 15, 0.92
		} else if bitrate < 20000 {
			return 6, 13, 0.96
		} else {
			return 2, 10, 1.00
		}
	}
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
	case "very high", "veryhigh":
		return QualityPresets[3].Bitrate
	case "ultra":
		return QualityPresets[4].Bitrate
	case "extreme":
		return QualityPresets[5].Bitrate
	case "max":
		return QualityPresets[6].Bitrate
	case "insane":
		return QualityPresets[7].Bitrate
	}

	// Try exact match
	if preset := QualityByName(value); preset != nil {
		return preset.Bitrate
	}

	// Default to medium
	return QualityPresets[DefaultQualityIndex()].Bitrate
}
