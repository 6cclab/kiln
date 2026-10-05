package imgpath

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // register the PNG decoder with image.Decode/DecodeConfig
	"os"
)

// MaxImagePixels bounds the declared width*height of an oversized image
// kiln will attempt to downscale. It is checked against image.DecodeConfig
// (which only reads a format header, never the pixel data) before any
// full decode, so a file that merely claims an enormous resolution — a
// decompression-bomb shape, not a real photo — is refused immediately
// rather than driving an allocation sized by an attacker-controlled
// number. 50 megapixels is comfortably above any real screenshot or
// camera photo kiln is likely to be handed.
const MaxImagePixels = 50_000_000

// downscaleAndFit decodes an oversized (over MaxImageBytes) image already
// known to be within MaxImagePixels, shrinks it until its re-encoded form
// fits maxBytes, and returns the fitted bytes and mime type. ok is false
// when no combination of scale and quality it tried got under maxBytes,
// or the file could not be decoded (format not recognised, or corrupt) —
// either way the caller refuses rather than attaching an oversized image.
func downscaleAndFit(path string, maxBytes int) (data []byte, mime string, ok bool) {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return nil, "", false
	}
	defer f.Close()

	img, format, err := image.Decode(f)
	if err != nil {
		return nil, "", false
	}
	if format != "png" && format != "jpeg" {
		// No encoder/decoder pairing kiln can re-size (gif, webp, bmp):
		// nothing left to try.
		return nil, "", false
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil, "", false
	}

	// Each step shrinks the linear dimensions further; re-encoding as
	// JPEG (not necessarily the original format) at a shrinking quality
	// is what actually controls output size — a downscaled lossless PNG
	// of a photo-like screenshot can still be large, where JPEG's quality
	// knob reliably trades size for fidelity.
	for _, scale := range []float64{1, 0.75, 0.5, 0.35, 0.25, 0.15, 0.1} {
		nw := max(1, int(float64(w)*scale))
		nh := max(1, int(float64(h)*scale))
		scaled := img
		if scale != 1 {
			scaled = boxDownscale(img, nw, nh)
		}
		for _, quality := range []int{85, 70, 55, 40} {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: quality}); err != nil {
				return nil, "", false
			}
			if buf.Len() <= maxBytes {
				return buf.Bytes(), "image/jpeg", true
			}
		}
	}
	return nil, "", false
}

// boxDownscale resizes img to exactly newW x newH using box/area
// averaging: each destination pixel is the average of the block of
// source pixels it covers. kiln has no image-resampling dependency in
// go.mod, so this is a small, dependency-free stand-in rather than a
// general-purpose resampler — good enough for shrinking a screenshot
// before re-encoding, not meant for photographic quality work.
func boxDownscale(img image.Image, newW, newH int) *image.NRGBA {
	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, newW, newH))
	for dy := 0; dy < newH; dy++ {
		sy0 := dy * srcH / newH
		sy1 := (dy + 1) * srcH / newH
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		if sy1 > srcH {
			sy1 = srcH
		}
		for dx := 0; dx < newW; dx++ {
			sx0 := dx * srcW / newW
			sx1 := (dx + 1) * srcW / newW
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			if sx1 > srcW {
				sx1 = srcW
			}
			var rSum, gSum, bSum, aSum, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					r, g, b, a := img.At(bounds.Min.X+sx, bounds.Min.Y+sy).RGBA()
					rSum += uint64(r)
					gSum += uint64(g)
					bSum += uint64(b)
					aSum += uint64(a)
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			// img.At(...).RGBA() returns alpha-premultiplied 16-bit
			// components; averaging them and storing straight into NRGBA
			// (non-premultiplied) is an approximation that is exact for
			// fully opaque source pixels — the common case for a
			// screenshot or photo — and only slightly off at a
			// partially-transparent edge, which a "shrink it to fit"
			// path does not need to get exactly right.
			dst.SetNRGBA(dx, dy, color.NRGBA{
				R: uint8((rSum / n) >> 8),
				G: uint8((gSum / n) >> 8),
				B: uint8((bSum / n) >> 8),
				A: uint8((aSum / n) >> 8),
			})
		}
	}
	return dst
}

// decodeImageConfig reads path's format header only (image.DecodeConfig
// never touches pixel data) and reports its declared width, height and
// format name ("png", "jpeg", or empty when unrecognised/unreadable).
// Used to check MaxImagePixels before any full decode is attempted, so a
// file merely claiming an enormous resolution never drives an allocation
// sized by that claim.
func decodeImageConfig(path string) (width, height int, format string, ok bool) {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return 0, 0, "", false
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, "", false
	}
	return cfg.Width, cfg.Height, format, true
}

// formatMegapixels renders w*h as a human megapixel figure for a refusal
// message.
func formatMegapixels(w, h int) string {
	return fmt.Sprintf("%.1f", float64(w)*float64(h)/1_000_000)
}
