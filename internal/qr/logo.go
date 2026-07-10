package qr

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	_ "image/png" // register PNG decoder
	"log/slog"
	"os"

	xdraw "golang.org/x/image/draw"
)

//go:embed assets/logo.png
var defaultLogoPNG []byte

// MaxLogoScale is the hard ceiling on logo size relative to the QR width.
// Larger than this and even error-correction level H can't reliably recover.
const MaxLogoScale = 0.22

// LogoOptions controls how WithLogo composites the logo onto a QR base. Zero
// values fall through to sensible defaults.
type LogoOptions struct {
	// ScalePct is the linear width of the logo as a fraction of the QR width.
	// Default 0.18. Clamped to MaxLogoScale.
	ScalePct float64

	// Padding is the width in pixels of the soft halo around the logo's
	// SHAPE (alpha silhouette), filled with BgColor. Default = ~4% of the
	// logo size. Set to a negative value to skip the halo entirely (logo
	// composited directly on top of QR modules).
	Padding int

	// CornerRadius is retained for backwards compatibility but no longer
	// used — the halo now follows the logo's alpha silhouette, not a
	// rectangle.
	CornerRadius int

	// BgColor is the halo color. Default = opaque white. Set BgColor.A == 0
	// to skip the halo even when Padding > 0.
	BgColor color.Color
}

// LoadLogo reads a logo image from disk, falling back to the embedded default
// if `path` is empty. Returns (nil, nil) silently if both the path is empty
// AND the embedded asset can't be decoded — the caller should treat nil as
// "render plain QRs."
func LoadLogo(path string, logger *slog.Logger) image.Image {
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			logger.Warn("qr_logo_open_failed_using_default", slog.String("path", path), slog.Any("err", err))
		} else {
			defer f.Close()
			img, _, err := image.Decode(f)
			if err != nil {
				logger.Warn("qr_logo_decode_failed_using_default", slog.String("path", path), slog.Any("err", err))
			} else {
				return img
			}
		}
	}
	img, _, err := image.Decode(bytes.NewReader(defaultLogoPNG))
	if err != nil {
		logger.Warn("qr_default_logo_decode_failed_rendering_plain", slog.Any("err", err))
		return nil
	}
	return img
}

// WithLogo returns a new RGBA image: the QR base with the logo centered on
// top, optionally surrounded by a halo that follows the logo's alpha
// silhouette (not a rectangle) so the result reads as "part of" the QR
// rather than a sticker pasted on top. The base is not mutated.
//
// Hard rules enforced here:
//   - logo linear size <= MaxLogoScale * base.Width()
//   - center placement (never overlaps finder patterns at the three corners)
//   - halo (if any) follows the logo's alpha shape, so the QR modules read
//     right up to the logo's actual contour
func WithLogo(base image.Image, logo image.Image, opts LogoOptions) image.Image {
	if base == nil {
		return nil
	}
	out := image.NewRGBA(base.Bounds())
	xdraw.Copy(out, image.Point{}, base, base.Bounds(), xdraw.Src, nil)
	if logo == nil {
		return out
	}

	scale := opts.ScalePct
	if scale <= 0 {
		scale = 0.18
	}
	if scale > MaxLogoScale {
		scale = MaxLogoScale
	}
	bw, bh := out.Bounds().Dx(), out.Bounds().Dy()
	logoSize := int(float64(min(bw, bh)) * scale)
	if logoSize < 1 {
		return out
	}

	// Padding default: small halo proportional to logo size. Negative skips
	// the halo entirely (naked logo on modules).
	padding := opts.Padding
	switch {
	case padding < 0:
		padding = 0
	case padding == 0:
		padding = max(2, logoSize*4/100)
	}
	bg := opts.BgColor
	if bg == nil {
		bg = color.RGBA{0xff, 0xff, 0xff, 0xff}
	}

	// Resize logo once; reuse the alpha for both the halo silhouette and
	// the final composite.
	resized := image.NewRGBA(image.Rect(0, 0, logoSize, logoSize))
	xdraw.CatmullRom.Scale(resized, resized.Bounds(), logo, logo.Bounds(), xdraw.Over, nil)

	logoX := (bw - logoSize) / 2
	logoY := (bh - logoSize) / 2

	if padding > 0 && alphaIsVisible(bg) {
		drawShapeFollowingHalo(out, resized, logoX, logoY, padding, bg)
	}

	xdraw.Draw(out, image.Rect(logoX, logoY, logoX+logoSize, logoY+logoSize), resized, image.Point{}, xdraw.Over)
	return out
}

// drawShapeFollowingHalo paints `bg` onto every pixel within `padding` of an
// opaque pixel of `logo`, but ONLY into the destination (not back into logo
// pixels themselves — the logo draws on top afterwards). Result is a halo
// that hugs the logo's silhouette, not a rectangular frame.
func drawShapeFollowingHalo(dst *image.RGBA, logo *image.RGBA, ox, oy, padding int, bg color.Color) {
	const alphaThreshold uint32 = 0x4000 // ~25% — ignore anti-aliased fringes when deciding "this is logo"
	lw, lh := logo.Bounds().Dx(), logo.Bounds().Dy()
	pad2 := padding * padding

	// Pass 1: collect the opaque mask of `logo` so the inner loop in pass 2
	// reads from a tight bool slice instead of decoding RGBA every time.
	mask := make([]bool, lw*lh)
	for y := 0; y < lh; y++ {
		for x := 0; x < lw; x++ {
			_, _, _, a := logo.At(x, y).RGBA()
			if a >= alphaThreshold {
				mask[y*lw+x] = true
			}
		}
	}

	// Pass 2: every pixel in the (logo + padding) bbox checks whether any
	// opaque mask pixel is within `padding` Euclidean distance.
	for dy := -padding; dy < lh+padding; dy++ {
		for dx := -padding; dx < lw+padding; dx++ {
			if dx >= 0 && dx < lw && dy >= 0 && dy < lh && mask[dy*lw+dx] {
				// Don't recolour pixels under the logo itself; the logo
				// composite that follows will draw there.
				continue
			}
			if nearOpaque(mask, lw, lh, dx, dy, padding, pad2) {
				dst.Set(ox+dx, oy+dy, bg)
			}
		}
	}
}

func nearOpaque(mask []bool, w, h, cx, cy, r, r2 int) bool {
	x0 := cx - r
	if x0 < 0 {
		x0 = 0
	}
	y0 := cy - r
	if y0 < 0 {
		y0 = 0
	}
	x1 := cx + r
	if x1 >= w {
		x1 = w - 1
	}
	y1 := cy + r
	if y1 >= h {
		y1 = h - 1
	}
	for y := y0; y <= y1; y++ {
		dy := y - cy
		row := y * w
		for x := x0; x <= x1; x++ {
			dx := x - cx
			if dx*dx+dy*dy > r2 {
				continue
			}
			if mask[row+x] {
				return true
			}
		}
	}
	return false
}

func alphaIsVisible(c color.Color) bool {
	_, _, _, a := c.RGBA()
	return a > 0
}

// DecodeLogoBytes is a small convenience for tests / callers that have raw
// PNG bytes rather than an image.Image.
func DecodeLogoBytes(b []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("decode logo: %w", err)
	}
	return img, nil
}
