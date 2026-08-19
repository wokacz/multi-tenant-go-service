package files

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestDetectReadsMagicBytesNotTheName(t *testing.T) {
	png := minimalPNG()
	if got := Detect(png); got != "image/png" {
		t.Errorf("Detect(png) = %q, want image/png", got)
	}

	if got := Detect([]byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")); got != "application/pdf" {
		t.Errorf("Detect(pdf) = %q, want application/pdf", got)
	}

	if got := Detect([]byte("MZ\x90\x00this is not a picture")); got != "application/x-msdownload" {
		t.Errorf("Detect(mz) = %q, want application/x-msdownload", got)
	}

	if got := Detect([]byte("hello, world\n")); got != "text/plain" {
		t.Errorf("Detect(text) = %q, want text/plain", got)
	}
}

func TestDetectIdentifiesOOXMLFromTheZipContents(t *testing.T) {
	got := Detect(minimalOOXML("xl/workbook.xml"))
	want := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	if got != want {
		t.Errorf("Detect(xlsx) = %q, want %q", got, want)
	}

	if got := Detect(minimalOOXML("readme.txt")); got != "application/zip" {
		t.Errorf("Detect(zip) = %q, want application/zip", got)
	}
}

func TestCanonicalMediaTypeStripsParameters(t *testing.T) {
	got := CanonicalMediaType("Image/JPEG; charset=binary")
	if got != "image/jpeg" {
		t.Errorf("CanonicalMediaType() = %q, want image/jpeg", got)
	}
}

func TestSanitizeNameRejectsPathsAndControls(t *testing.T) {
	got, err := sanitizeName("../secret.pdf")
	if err != nil {
		t.Fatalf("sanitizeName(../secret.pdf) = %v, want the base name", err)
	}

	if got != "secret.pdf" {
		t.Errorf("sanitizeName(../secret.pdf) = %q, want secret.pdf", got)
	}

	got, err = sanitizeName("C:\\uploads\\report.PDF")
	if err != nil {
		t.Fatalf("sanitizeName(windows path) = %v", err)
	}

	if got != "report.PDF" {
		t.Errorf("sanitizeName() = %q, want report.PDF", got)
	}

	if _, err := sanitizeName("a\x00b.pdf"); err == nil {
		t.Fatal("sanitizeName(null byte) = nil, want ErrNameInvalid")
	}

	if _, err := sanitizeName(strings.Repeat("a", MaxNameLength+1)); err == nil {
		t.Fatal("sanitizeName(too long) = nil, want ErrNameInvalid")
	}
}

func minimalPNG() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
}

func minimalOOXML(inner string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	f, err := w.Create("[Content_Types].xml")
	if err != nil {
		panic(err)
	}

	_, _ = f.Write([]byte("<Types></Types>"))

	f, err = w.Create(inner)
	if err != nil {
		panic(err)
	}

	_, _ = f.Write([]byte("not-a-real-office-part"))
	if err := w.Close(); err != nil {
		panic(err)
	}

	return buf.Bytes()
}
