package encoding

import "fmt"

// NewEncoder creates a new encoder based on the codec type
func NewEncoder(codecType CodecType, config EncoderConfig) (VideoEncoder, error) {
	switch codecType {
	case CodecVP8:
		return NewVP8Encoder(config), nil
	case CodecVP9:
		return NewVP9Encoder(config), nil
	case CodecH264:
		if !IsVideoToolboxAvailable() {
			return nil, fmt.Errorf("VideoToolbox H.264 encoding not available")
		}
		return NewVideoToolboxEncoder(config), nil
	default:
		return nil, fmt.Errorf("unknown codec type: %s", codecType)
	}
}

// GetAvailableCodecs returns information about available codecs
func GetAvailableCodecs() []CodecInfo {
	codecs := []CodecInfo{
		{
			Type:        CodecVP8,
			Name:        "VP8",
			Description: "Software VP8 encoder",
			IsHardware:  false,
		},
		{
			Type:        CodecVP9,
			Name:        "VP9",
			Description: "Software VP9 encoder",
			IsHardware:  false,
		},
	}

	if IsVideoToolboxAvailable() {
		codecs = append(codecs, CodecInfo{
			Type:        CodecH264,
			Name:        "H.264",
			Description: "Hardware H.264 encoder (VideoToolbox)",
			IsHardware:  true,
		})
	}

	return codecs
}

// GetMimeType returns the WebRTC mime type for a codec
func GetMimeType(codecType CodecType) string {
	switch codecType {
	case CodecVP8:
		return "video/VP8"
	case CodecVP9:
		return "video/VP9"
	case CodecH264:
		return "video/H264"
	default:
		return "video/VP8"
	}
}

// ParseCodecType parses a string into a CodecType
func ParseCodecType(s string) (CodecType, error) {
	switch s {
	case "vp8", "VP8":
		return CodecVP8, nil
	case "vp9", "VP9":
		return CodecVP9, nil
	case "h264", "H264", "h.264", "H.264":
		return CodecH264, nil
	default:
		return "", fmt.Errorf("unknown codec: %s", s)
	}
}
