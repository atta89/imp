package qr

import (
	"bytes"
	"testing"
)

func TestLabelsPDF_MultipleLabelsHasPdfMagic(t *testing.T) {
	labels := []Label{
		{Content: "https://x/scan/aaa", Tag: "LAP-0001", Name: "MacBook Pro 14", Category: "Laptop", HomeVenue: "HQ"},
		{Content: "https://x/scan/bbb", Tag: "LAP-0002", Name: "MacBook Air 13", Category: "Laptop", HomeVenue: "HQ"},
	}
	out, err := LabelsPDF(labels)
	if err != nil {
		t.Fatalf("LabelsPDF: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Errorf("output does not start with PDF magic")
	}
	if len(out) < 1000 {
		t.Errorf("output suspiciously small: %d bytes", len(out))
	}
}

func TestLabelsPDF_EmptyReturnsError(t *testing.T) {
	if _, err := LabelsPDF(nil); err == nil {
		t.Error("expected error for empty labels")
	}
}

func TestLabelsPDF_OverflowAddsSecondPage(t *testing.T) {
	// 18 per page (3 cols × 6 rows). 19 → 2 pages.
	labels := make([]Label, 19)
	for i := range labels {
		labels[i] = Label{Content: "https://x/scan/t", Tag: "T-0001", Name: "n", Category: "c", HomeVenue: "v"}
	}
	out, err := LabelsPDF(labels)
	if err != nil {
		t.Fatalf("LabelsPDF: %v", err)
	}
	// Lightweight check — fpdf writes "/Count N" for the pages dict; assert N >= 2.
	if !bytes.Contains(out, []byte("/Count 2")) {
		t.Errorf("expected 2-page PDF (got %d bytes; no /Count 2)", len(out))
	}
}
