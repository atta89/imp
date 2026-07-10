package qr

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/makiuchi-d/gozxing"
	gozxqr "github.com/makiuchi-d/gozxing/qrcode"
	xdraw "golang.org/x/image/draw"
)

const sampleURL = "https://app.example.com/scan/abc123_xyz-456"

// decode runs the gozxing QR reader over the given image and returns the
// decoded payload string, or t.Fatal's.
func decode(t *testing.T, img image.Image) string {
	t.Helper()
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatalf("bitmap: %v", err)
	}
	res, err := gozxqr.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		t.Fatalf("qr decode: %v", err)
	}
	return res.GetText()
}

func decodeBytes(t *testing.T, b []byte) string {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	return decode(t, img)
}

func TestPNGWithLogo_RoundTripsDefaultLogo(t *testing.T) {
	logo, err := DecodeLogoBytes(defaultLogoPNG)
	if err != nil {
		t.Fatalf("decode embedded logo: %v", err)
	}
	out, err := PNGWithLogo(sampleURL, 1024, logo, LogoOptions{})
	if err != nil {
		t.Fatalf("PNGWithLogo: %v", err)
	}
	got := decodeBytes(t, out)
	if got != sampleURL {
		t.Errorf("decoded payload mismatch:\n got:  %q\n want: %q", got, sampleURL)
	}
}

func TestPNGWithLogo_RoundTripsAfterDownscaleTo128px(t *testing.T) {
	// A logo too large for the error-correction budget will scan at full
	// size but fail after downscaling. Pins the defaults as robust enough
	// for label-print resolutions.
	logo, err := DecodeLogoBytes(defaultLogoPNG)
	if err != nil {
		t.Fatalf("decode embedded logo: %v", err)
	}
	out, err := PNGWithLogo(sampleURL, 1024, logo, LogoOptions{})
	if err != nil {
		t.Fatalf("PNGWithLogo: %v", err)
	}
	full, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	small := image.NewRGBA(image.Rect(0, 0, 128, 128))
	xdraw.CatmullRom.Scale(small, small.Bounds(), full, full.Bounds(), xdraw.Src, nil)
	got := decode(t, small)
	if got != sampleURL {
		t.Errorf("downscaled decode mismatch:\n got:  %q\n want: %q", got, sampleURL)
	}
}

func TestPNGWithLogo_NilLogoStillDecodes(t *testing.T) {
	out, err := PNGWithLogo(sampleURL, 1024, nil, LogoOptions{})
	if err != nil {
		t.Fatalf("PNGWithLogo: %v", err)
	}
	got := decodeBytes(t, out)
	if got != sampleURL {
		t.Errorf("nil-logo decode mismatch:\n got:  %q\n want: %q", got, sampleURL)
	}
}

// TestPNGWithLogo_OversizeIsClamped — asking for a 50% logo (way past the
// 30% redundancy budget) gets clamped by MaxLogoScale (22%) and still decodes.
// If a future change removed the clamp this test would fail.
func TestPNGWithLogo_OversizeIsClamped(t *testing.T) {
	logo, err := DecodeLogoBytes(defaultLogoPNG)
	if err != nil {
		t.Fatalf("decode embedded logo: %v", err)
	}
	out, err := PNGWithLogo(sampleURL, 1024, logo, LogoOptions{ScalePct: 0.50})
	if err != nil {
		t.Fatalf("PNGWithLogo: %v", err)
	}
	got := decodeBytes(t, out)
	if got != sampleURL {
		t.Errorf("oversize-clamp decode mismatch:\n got:  %q\n want: %q", got, sampleURL)
	}
}

// TestPNGWithLogo_WritesSample emits a sample PNG to a tempdir path printed
// in the test log, so a human can eyeball it: `go test -v -run Sample ./internal/qr`
func TestPNGWithLogo_WritesSample(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping sample-write in -short mode")
	}
	logo, err := DecodeLogoBytes(defaultLogoPNG)
	if err != nil {
		t.Fatalf("decode embedded logo: %v", err)
	}
	out, err := PNGWithLogo(sampleURL, 1024, logo, LogoOptions{})
	if err != nil {
		t.Fatalf("PNGWithLogo: %v", err)
	}
	path := filepath.Join(t.TempDir(), "sample_qr_with_logo.png")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	t.Logf("wrote sample QR to %s (%d bytes)", path, len(out))
}
