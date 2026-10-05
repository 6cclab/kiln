package imgpath

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noisyPNG builds a w x h PNG of pseudo-random pixels: noise defeats
// PNG's DEFLATE compression, so this reliably produces a file bigger
// than MaxImageBytes without needing a huge resolution.
func noisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{
				R: uint8(r.Intn(256)), G: uint8(r.Intn(256)), B: uint8(r.Intn(256)), A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode noisy png: %v", err)
	}
	return buf.Bytes()
}

// An oversized (over MaxImageBytes) but ordinary PNG is downscaled and
// re-encoded rather than refused: the attached image fits the limit and
// is meaningfully smaller than the original file.
func TestResolveDownscalesOversizedPNG(t *testing.T) {
	dir := t.TempDir()
	data := noisyPNG(t, 1600, 1600)
	if len(data) <= MaxImageBytes {
		t.Fatalf("test PNG is only %d bytes, want it over the %d-byte cap (noise didn't inflate it enough)", len(data), MaxImageBytes)
	}
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	v, ok := Resolve(path, dir)
	if !ok {
		t.Fatal("want ok (recognised extension)")
	}
	if v.Refused != "" {
		t.Fatalf("Refused = %q, want the image downscaled instead", v.Refused)
	}
	if v.Image == nil {
		t.Fatal("want an attached (downscaled) image")
	}
	decoded, err := base64.StdEncoding.DecodeString(v.Image.Data)
	if err != nil {
		t.Fatalf("decode attached image: %v", err)
	}
	if len(decoded) > MaxImageBytes {
		t.Errorf("downscaled image is %d bytes, want at most %d (MaxImageBytes)", len(decoded), MaxImageBytes)
	}
	if len(decoded) >= len(data) {
		t.Errorf("downscaled image (%d bytes) is not smaller than the original (%d bytes)", len(decoded), len(data))
	}
	if v.Image.MimeType != "image/jpeg" {
		t.Errorf("MimeType = %q, want image/jpeg (kiln re-encodes to control size)", v.Image.MimeType)
	}
}

// crcChunk appends a length-prefixed, CRC-suffixed PNG chunk to buf,
// exactly as the PNG spec requires: 4-byte big-endian length (of data
// only), the 4-byte type, the data, then a CRC-32 (IEEE) over type+data.
func crcChunk(buf []byte, typ string, data []byte) []byte {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	buf = append(buf, lenBuf[:]...)
	buf = append(buf, typ...)
	buf = append(buf, data...)
	crc := crc32.ChecksumIEEE(append([]byte(typ), data...))
	var crcBuf [4]byte
	binary.BigEndian.PutUint32(crcBuf[:], crc)
	return append(buf, crcBuf[:]...)
}

// hugeDimensionPNG builds a file that is a syntactically valid PNG
// signature + IHDR chunk declaring an enormous width/height (enough to
// trip MaxImagePixels), padded with junk bytes (never parsed as PNG
// data) until it is also over MaxImageBytes in raw file size — so
// Resolve takes the oversized-image path, and image.DecodeConfig can
// read the (valid) IHDR without needing, or getting, any real pixel
// data.
func hugeDimensionPNG(t *testing.T, width, height uint32) []byte {
	t.Helper()
	buf := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a} // PNG signature
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8  // bit depth
	ihdr[9] = 2  // color type: truecolor
	ihdr[10] = 0 // compression
	ihdr[11] = 0 // filter
	ihdr[12] = 0 // interlace
	buf = crcChunk(buf, "IHDR", ihdr)
	if int64(len(buf)) < MaxImageBytes+1 {
		buf = append(buf, make([]byte, MaxImageBytes+1-int64(len(buf)))...)
	}
	return buf
}

// A file whose declared PNG dimensions exceed MaxImagePixels is refused
// by that header alone: image.Decode (which would try to allocate and
// decompress the full image) is never reached. Checked by making the
// file an invalid PNG past the IHDR chunk (no real IDAT) — if the code
// under test ever called image.Decode on it, Decode would return a
// format error, which this test tells apart from the dimension refusal
// by its message.
func TestResolveRefusesHugeDimensionsWithoutDecoding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bomb.png")
	data := hugeDimensionPNG(t, 50000, 50000) // 2.5 billion declared pixels
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	withinImgpathTest(t, "Resolve on a huge-declared-dimension PNG", func() {
		v, ok := Resolve(path, dir)
		if !ok {
			t.Error("want ok (recognised extension)")
			return
		}
		if v.Image != nil {
			t.Error("a 2.5-billion-pixel image must not attach")
		}
		if !strings.Contains(v.Refused, "megapixel") {
			t.Errorf("Refused = %q, want it to name the declared-dimension refusal (not a decode-failure message)", v.Refused)
		}
	})
}
