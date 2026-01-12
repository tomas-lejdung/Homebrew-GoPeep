package config

import (
	"strings"

	"github.com/tomaslejdung/gopeep/internal/encoding"
)

// CodecInfo describes a codec option for the UI
type CodecInfo struct {
	Type        encoding.CodecType
	Name        string // Display name
	Description string // Short description
	IsHardware  bool   // Whether this uses hardware encoding
}

// AvailableCodecs - will be populated at runtime based on what's available
var AvailableCodecs []CodecInfo

// DefaultCodecIndex returns the index of the default codec (VP8)
func DefaultCodecIndex() int {
	return 0 // VP8 is default
}

// CodecByType finds a codec by type
func CodecByType(codecType encoding.CodecType) *CodecInfo {
	for i := range AvailableCodecs {
		if AvailableCodecs[i].Type == codecType {
			return &AvailableCodecs[i]
		}
	}
	return nil
}

// ParseCodecFlag parses the --codec flag value
func ParseCodecFlag(value string) encoding.CodecType {
	value = strings.ToLower(value)
	switch value {
	case "vp8":
		return encoding.CodecVP8
	case "vp9":
		return encoding.CodecVP9
	case "h264", "h.264", "avc":
		return encoding.CodecH264
	default:
		return encoding.CodecVP8
	}
}

// InitAvailableCodecs detects and initializes the list of available codecs
func InitAvailableCodecs() {
	AvailableCodecs = []CodecInfo{
		{
			Type:        encoding.CodecVP8,
			Name:        "VP8",
			Description: "fast, compatible",
			IsHardware:  false,
		},
		{
			Type:        encoding.CodecVP9,
			Name:        "VP9",
			Description: "better quality",
			IsHardware:  false,
		},
	}

	// Add H.264 - prefer hardware if available
	if encoding.IsVideoToolboxAvailable() {
		AvailableCodecs = append(AvailableCodecs, CodecInfo{
			Type:        encoding.CodecH264,
			Name:        "H.264",
			Description: "hardware",
			IsHardware:  true,
		})
	} else {
		AvailableCodecs = append(AvailableCodecs, CodecInfo{
			Type:        encoding.CodecH264,
			Name:        "H.264",
			Description: "software",
			IsHardware:  false,
		})
	}
}
