package qr

import (
	"bytes"
	"testing"
)

// TestRenderLabelQR_NoLogoDecodes pins the historic no-logo behaviour:
// 512px Medium PNG, round-trips cleanly.
func TestRenderLabelQR_NoLogoDecodes(t *testing.T) {
	out, err := renderLabelQR(sampleURL, nil, LogoOptions{})
	if err != nil {
		t.Fatalf("renderLabelQR: %v", err)
	}
	if got := decodeBytes(t, out); got != sampleURL {
		t.Errorf("no-logo: decoded %q, want %q", got, sampleURL)
	}
}

// TestRenderLabelQR_WithLogoDecodes is the contract the bulk PDF leans on:
// every cell that the renderer produces must round-trip through a scanner.
// If MaxLogoScale, defaults, or the Highest-vs-High constant ever change,
// this fails before users discover unscanned labels in the field.
func TestRenderLabelQR_WithLogoDecodes(t *testing.T) {
	logo, err := DecodeLogoBytes(defaultLogoPNG)
	if err != nil {
		t.Fatalf("decode embedded logo: %v", err)
	}
	out, err := renderLabelQR(sampleURL, logo, LogoOptions{})
	if err != nil {
		t.Fatalf("renderLabelQR: %v", err)
	}
	if got := decodeBytes(t, out); got != sampleURL {
		t.Errorf("with-logo: decoded %q, want %q", got, sampleURL)
	}
}

// TestLabelsPDFWith_LogoOptionEmbedsPNGs is a smoke test on the full bulk
// path with the logo wired in. We don't extract+decode the embedded PNGs
// from the PDF binary (fpdf compresses image streams; extracting is fragile);
// the round-trip guarantee comes from TestRenderLabelQR_WithLogoDecodes which
// exercises the exact PNG generator the PDF embeds.
func TestLabelsPDFWith_LogoOptionEmbedsPNGs(t *testing.T) {
	logo, err := DecodeLogoBytes(defaultLogoPNG)
	if err != nil {
		t.Fatalf("decode embedded logo: %v", err)
	}
	labels := []Label{
		{Content: sampleURL, Tag: "LAP-0001", Name: "MacBook Pro", Category: "Laptop", HomeVenue: "HQ"},
		{Content: sampleURL, Tag: "LAP-0002", Name: "MacBook Air", Category: "Laptop", HomeVenue: "HQ"},
	}
	withLogo, err := LabelsPDFWith(labels, LabelsPDFOptions{Logo: logo})
	if err != nil {
		t.Fatalf("LabelsPDFWith: %v", err)
	}
	if !bytes.HasPrefix(withLogo, []byte("%PDF-")) {
		t.Error("no PDF magic in with-logo output")
	}

	// Logo'd render uses 1024px Highest-correction PNGs; no-logo uses
	// 512px Medium. The byte counts diverge meaningfully — assert that as
	// a sanity check the option was actually honoured.
	noLogo, err := LabelsPDFWith(labels, LabelsPDFOptions{})
	if err != nil {
		t.Fatalf("LabelsPDFWith no-logo: %v", err)
	}
	if len(withLogo) <= len(noLogo) {
		t.Errorf("expected with-logo PDF larger than no-logo (with=%d, no=%d)", len(withLogo), len(noLogo))
	}
}
