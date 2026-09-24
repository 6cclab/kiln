package tools

import "encoding/base64"

// pngSignature is the 8-byte PNG file signature.
var pngSignature = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// detectSupportedImageMimeType sniffs buffer's magic bytes, mirroring
// image.js's detectSupportedImageMimeType: JPEG, PNG (rejecting animated
// APNG and the JPEG-XT-in-JPEG-container variant marked by byte 3 ==
// 0xF7), GIF, WebP and BMP (validated, not just RIFF/BM-prefixed).
func detectSupportedImageMimeType(buffer []byte) string {
	switch {
	case startsWith(buffer, []byte{0xff, 0xd8, 0xff}):
		if len(buffer) > 3 && buffer[3] == 0xf7 {
			return ""
		}
		return "image/jpeg"
	case startsWith(buffer, pngSignature):
		if isPNG(buffer) && !isAnimatedPNG(buffer) {
			return "image/png"
		}
		return ""
	case startsWithASCII(buffer, 0, "GIF87a") || startsWithASCII(buffer, 0, "GIF89a"):
		return "image/gif"
	case startsWithASCII(buffer, 0, "RIFF") && startsWithASCII(buffer, 8, "WEBP"):
		return "image/webp"
	case startsWithASCII(buffer, 0, "BM") && isBMP(buffer):
		return "image/bmp"
	default:
		return ""
	}
}

// encodeImageBase64 base64-encodes bytes for an ImageContent block.
func encodeImageBase64(bytes []byte) string {
	return base64.StdEncoding.EncodeToString(bytes)
}

func isPNG(buffer []byte) bool {
	return len(buffer) >= 16 && readUint32BE(buffer, len(pngSignature)) == 13 && startsWithASCII(buffer, 12, "IHDR")
}

func isAnimatedPNG(buffer []byte) bool {
	offset := len(pngSignature)
	for offset+8 <= len(buffer) {
		chunkLength := readUint32BE(buffer, offset)
		chunkTypeOffset := offset + 4
		if startsWithASCII(buffer, chunkTypeOffset, "acTL") {
			return true
		}
		if startsWithASCII(buffer, chunkTypeOffset, "IDAT") {
			return false
		}
		nextOffset := offset + 8 + int(chunkLength) + 4
		if nextOffset <= offset || nextOffset > len(buffer) {
			return false
		}
		offset = nextOffset
	}
	return false
}

func isBMP(buffer []byte) bool {
	if len(buffer) < 26 {
		return false
	}
	declaredFileSize := readUint32LE(buffer, 2)
	pixelDataOffset := readUint32LE(buffer, 10)
	dibHeaderSize := readUint32LE(buffer, 14)
	if declaredFileSize != 0 && declaredFileSize < 26 {
		return false
	}
	if pixelDataOffset < 14+dibHeaderSize {
		return false
	}
	if declaredFileSize != 0 && pixelDataOffset >= declaredFileSize {
		return false
	}
	var colorPlanes, bitsPerPixel uint32
	switch {
	case dibHeaderSize == 12:
		colorPlanes = uint32(readUint16LE(buffer, 22))
		bitsPerPixel = uint32(readUint16LE(buffer, 24))
	case dibHeaderSize >= 40 && dibHeaderSize <= 124:
		if len(buffer) < 30 {
			return false
		}
		colorPlanes = uint32(readUint16LE(buffer, 26))
		bitsPerPixel = uint32(readUint16LE(buffer, 28))
	default:
		return false
	}
	if colorPlanes != 1 {
		return false
	}
	switch bitsPerPixel {
	case 1, 4, 8, 16, 24, 32:
		return true
	default:
		return false
	}
}

func readUint16LE(buffer []byte, offset int) uint16 {
	return uint16(safeByte(buffer, offset)) | uint16(safeByte(buffer, offset+1))<<8
}

func readUint32BE(buffer []byte, offset int) uint32 {
	return uint32(safeByte(buffer, offset))<<24 | uint32(safeByte(buffer, offset+1))<<16 |
		uint32(safeByte(buffer, offset+2))<<8 | uint32(safeByte(buffer, offset+3))
}

func readUint32LE(buffer []byte, offset int) uint32 {
	return uint32(safeByte(buffer, offset)) | uint32(safeByte(buffer, offset+1))<<8 |
		uint32(safeByte(buffer, offset+2))<<16 | uint32(safeByte(buffer, offset+3))<<24
}

func safeByte(buffer []byte, offset int) byte {
	if offset < 0 || offset >= len(buffer) {
		return 0
	}
	return buffer[offset]
}

func startsWith(buffer, prefix []byte) bool {
	if len(buffer) < len(prefix) {
		return false
	}
	for i, b := range prefix {
		if buffer[i] != b {
			return false
		}
	}
	return true
}

func startsWithASCII(buffer []byte, offset int, text string) bool {
	if len(buffer) < offset+len(text) {
		return false
	}
	for i := 0; i < len(text); i++ {
		if buffer[offset+i] != text[i] {
			return false
		}
	}
	return true
}
