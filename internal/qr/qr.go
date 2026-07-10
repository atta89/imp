// Package qr renders asset QR codes and printable label sheets.
package qr

import (
	"bytes"
	"fmt"
	"image"
	"image/png"

	"github.com/go-pdf/fpdf"
	qrcode "github.com/skip2/go-qrcode"
)

// PNG returns a plain (logo-less) QR PNG at `sizePx` pixels, encoded at
// error-correction level Medium (~15%). For codes that will carry a center
// logo overlay, use PNGWithLogo instead — it forces level Highest.
func PNG(content string, sizePx int) ([]byte, error) {
	if sizePx <= 0 {
		sizePx = 256
	}
	return qrcode.Encode(content, qrcode.Medium, sizePx)
}

// PNGWithLogo returns a PNG of a QR encoded at error-correction level Highest
// (~30%, qrcode.Highest — NOT qrcode.High which is only ~25%; using the wrong
// constant is the classic gotcha that produces logo'd codes which scan in
// previews and fail in the field) with `logo` composited dead-center on a
// padded backing. If `logo` is nil it falls through to the plain QR — but
// still at level Highest, so the output stays interchangeable.
func PNGWithLogo(content string, sizePx int, logo image.Image, opts LogoOptions) ([]byte, error) {
	if sizePx <= 0 {
		sizePx = 1024
	}
	q, err := qrcode.New(content, qrcode.Highest)
	if err != nil {
		return nil, fmt.Errorf("new qr: %w", err)
	}
	q.DisableBorder = false // keep the quiet zone
	base := q.Image(sizePx)
	final := WithLogo(base, logo, opts)
	var buf bytes.Buffer
	if err := png.Encode(&buf, final); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// Label describes one row on a bulk label sheet.
type Label struct {
	// Content is the URL the QR encodes (e.g. https://.../scan/<token>).
	Content string
	// Tag is the human-readable asset tag printed under the QR (e.g. LAP-0001).
	Tag       string
	Name      string
	Category  string
	HomeVenue string
}

// LabelsPDFOptions controls the bulk-label render. Zero values are safe;
// the no-logo defaults reproduce the original LabelsPDF behaviour bit-for-bit.
type LabelsPDFOptions struct {
	// Logo is optionally composited dead-center on every QR cell. nil = no
	// logo (cells render at error-correction Medium, smaller PNG).
	Logo image.Image

	// LogoOptions tunes how the logo is overlaid. Ignored if Logo is nil.
	LogoOptions LogoOptions
}

// LabelsPDF renders the given labels onto one or more A4 pages, 3 columns
// wide, with no center logo. Thin wrapper around LabelsPDFWith.
func LabelsPDF(labels []Label) ([]byte, error) {
	return LabelsPDFWith(labels, LabelsPDFOptions{})
}

// LabelsPDFWith is the configurable bulk renderer. When opts.Logo is set,
// every cell's QR is generated at error-correction level Highest (~30%) at
// 1024px (~50ms per cell extra) so the embedded logo stays crisp when scaled
// to the printed cell. When opts.Logo is nil it falls through to the original
// Medium-correction 512px render.
func LabelsPDFWith(labels []Label, opts LabelsPDFOptions) ([]byte, error) {
	if len(labels) == 0 {
		return nil, fmt.Errorf("no labels to render")
	}

	const (
		cols      = 3
		rows      = 6
		perPage   = cols * rows
		labelWmm  = 60.0
		labelHmm  = 45.0
		qrMm      = 26.0
		marginXmm = 15.0
		marginYmm = 15.0
	)

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)

	for i, l := range labels {
		if i%perPage == 0 {
			pdf.AddPage()
		}
		pos := i % perPage
		col := pos % cols
		row := pos / cols
		x := marginXmm + float64(col)*labelWmm
		y := marginYmm + float64(row)*labelHmm

		pngBytes, err := renderLabelQR(l.Content, opts.Logo, opts.LogoOptions)
		if err != nil {
			return nil, fmt.Errorf("render qr %d: %w", i, err)
		}
		imgName := fmt.Sprintf("qr-%d", i)
		imgOpts := fpdf.ImageOptions{ImageType: "png", ReadDpi: false}
		pdf.RegisterImageOptionsReader(imgName, imgOpts, bytes.NewReader(pngBytes))

		qrX := x + (labelWmm-qrMm)/2
		pdf.ImageOptions(imgName, qrX, y+2, qrMm, qrMm, false, imgOpts, 0, "")

		captionY := y + qrMm + 3
		pdf.SetFont("Helvetica", "B", 9)
		pdf.SetXY(x, captionY)
		pdf.CellFormat(labelWmm, 4, l.Tag, "", 0, "C", false, 0, "")

		pdf.SetFont("Helvetica", "", 7)
		pdf.SetXY(x, captionY+4)
		pdf.CellFormat(labelWmm, 3.5, truncate(l.Name, 32), "", 0, "C", false, 0, "")
		pdf.SetXY(x, captionY+7.5)
		pdf.CellFormat(labelWmm, 3.5, truncate(l.Category+" · "+l.HomeVenue, 32), "", 0, "C", false, 0, "")
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render pdf: %w", err)
	}
	return buf.Bytes(), nil
}

// renderLabelQR produces the PNG bytes that get embedded in one PDF cell.
// Exported via internal use so tests can decode and round-trip-verify
// without re-extracting from the PDF.
func renderLabelQR(content string, logo image.Image, opts LogoOptions) ([]byte, error) {
	if logo == nil {
		return PNG(content, 512)
	}
	return PNGWithLogo(content, 1024, logo, opts)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
