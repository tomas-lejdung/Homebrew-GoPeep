package encoding

import (
	"image"
	"time"

	"github.com/tomaslejdung/gopeep/internal/capture"
)

// CodecType represents the video codec to use
type CodecType string

const (
	CodecVP8  CodecType = "vp8"
	CodecVP9  CodecType = "vp9"
	CodecH264 CodecType = "h264"
)

// BGRAFrame is an alias to capture.BGRAFrame for convenience
type BGRAFrame = capture.BGRAFrame

// EncoderConfig holds encoder configuration
type EncoderConfig struct {
	Width   int
	Height  int
	FPS     int
	Bitrate int // in kbps
}

// DefaultEncoderConfig returns default encoder settings
func DefaultEncoderConfig() EncoderConfig {
	return EncoderConfig{
		Width:   1920,
		Height:  1080,
		FPS:     30,
		Bitrate: 2000, // 2 Mbps
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

	// SetBitrate changes the target bitrate (kbps) at runtime
	// The encoder may need to recreate internal state on next encode
	SetBitrate(bitrate int) error

	// SetQualityMode enables quality-priority encoding (true) vs performance/bandwidth-efficient (false)
	// Quality mode uses CQ/CRF rate control to maintain consistent visual quality
	// Performance mode uses CBR for bandwidth efficiency
	SetQualityMode(enabled bool, bitrate int) error
}

// CodecInfo describes a codec option for the UI
type CodecInfo struct {
	Type        CodecType
	Name        string // Display name
	Description string // Short description
	IsHardware  bool   // Whether this uses hardware encoding
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
