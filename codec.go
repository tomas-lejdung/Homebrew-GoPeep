package main

import (
	"image"
	"strings"
	"time"
)

// CodecType represents the video codec to use
type CodecType string

const (
	CodecVP8  CodecType = "vp8"
	CodecVP9  CodecType = "vp9"
	CodecH264 CodecType = "h264"
)

// CodecInfo describes a codec option for the UI
type CodecInfo struct {
	Type        CodecType
	Name        string // Display name
	Description string // Short description
	IsHardware  bool   // Whether this uses hardware encoding
}

// Available codecs - will be populated at runtime based on what's available
var AvailableCodecs []CodecInfo

// DefaultCodecIndex returns the index of the default codec (VP8)
func DefaultCodecIndex() int {
	return 0 // VP8 is default
}

// CodecByType finds a codec by type
func CodecByType(codecType CodecType) *CodecInfo {
	for i := range AvailableCodecs {
		if AvailableCodecs[i].Type == codecType {
			return &AvailableCodecs[i]
		}
	}
	return nil
}

// ParseCodecFlag parses the --codec flag value
func ParseCodecFlag(value string) CodecType {
	value = strings.ToLower(value)
	switch value {
	case "vp8":
		return CodecVP8
	case "vp9":
		return CodecVP9
	case "h264", "h.264", "avc":
		return CodecH264
	default:
		return CodecVP8
	}
}

// VideoEncoder is the interface that all video encoders must implement
type VideoEncoder interface {
	// Start initializes the encoder
	Start() error

	// Stop stops the encoder and releases resources
	Stop()

	// EncodeFrame encodes an RGBA image and returns the encoded data
	// Deprecated: Use EncodeBGRAFrame for better performance
	EncodeFrame(img *image.RGBA) ([]byte, error)

	// EncodeBGRAFrame encodes raw BGRA data directly (faster, no color conversion)
	EncodeBGRAFrame(frame *BGRAFrame) ([]byte, error)

	// GetSampleDuration returns the duration of one frame
	GetSampleDuration() time.Duration

	// GetCodecType returns the codec type
	GetCodecType() CodecType

	// IsHardwareAccelerated returns true if using hardware encoding
	IsHardwareAccelerated() bool
}

// EncoderFactory creates encoders based on codec type
type EncoderFactory struct{}

// NewEncoderFactory creates a new encoder factory
func NewEncoderFactory() *EncoderFactory {
	return &EncoderFactory{}
}

// CreateEncoder creates an encoder for the specified codec
func (f *EncoderFactory) CreateEncoder(codecType CodecType, fps, bitrate int) (VideoEncoder, error) {
	config := EncoderConfig{
		FPS:     fps,
		Bitrate: bitrate,
	}

	switch codecType {
	case CodecVP9:
		return NewVP9Encoder(config), nil
	case CodecH264:
		// Try hardware encoder first, fall back to software
		if IsVideoToolboxAvailable() {
			return NewVideoToolboxEncoder(config), nil
		}
		return NewH264Encoder(config), nil
	case CodecVP8:
		fallthrough
	default:
		return NewVP8Encoder(config), nil
	}
}

// InitAvailableCodecs detects and initializes the list of available codecs
func InitAvailableCodecs() {
	AvailableCodecs = []CodecInfo{
		{
			Type:        CodecVP8,
			Name:        "VP8",
			Description: "fast, compatible",
			IsHardware:  false,
		},
		{
			Type:        CodecVP9,
			Name:        "VP9",
			Description: "better quality",
			IsHardware:  false,
		},
	}

	// Add H.264 - prefer hardware if available
	if IsVideoToolboxAvailable() {
		AvailableCodecs = append(AvailableCodecs, CodecInfo{
			Type:        CodecH264,
			Name:        "H.264",
			Description: "hardware",
			IsHardware:  true,
		})
	} else {
		AvailableCodecs = append(AvailableCodecs, CodecInfo{
			Type:        CodecH264,
			Name:        "H.264",
			Description: "software",
			IsHardware:  false,
		})
	}
}

// IsVideoToolboxAvailable checks if VideoToolbox hardware encoding is available
// This will be implemented in encoder_videotoolbox.go
var IsVideoToolboxAvailable = func() bool {
	return false // Default, overridden by videotoolbox implementation
}
