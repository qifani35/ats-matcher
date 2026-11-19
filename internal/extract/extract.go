// Package extract turns a folder of CV files into structured candidates.
//
// Two extraction paths, and they are NOT the same problem:
//
//	DOCX   a zip archive; the text lives in word/document.xml. Pure stdlib, exact.
//	PDF    content streams, usually Flate-compressed. Text is drawn with
//	       Tj/TJ operators. A minimal parser covers files exported by Word,
//	       LibreOffice and most CV builders.
//
// Scanned PDFs and photos carry no text layer at all: those need OCR, which is
// a separate step (see OCRHint) because it changes the accuracy profile.
package extract

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Kind describes how the text was obtained, which matters for confidence:
// OCR output is noisier and its fields should be reviewed.
type Kind string

const (
	KindDOCX Kind = "docx"
	KindPDF  Kind = "pdf-text"
	KindScan Kind = "needs-ocr"
	KindText Kind = "text"
)

// Document is the result of reading one file.
type Document struct {
	Path  string
	Kind  Kind
	Text  string
	Pages int
	Warn  string
}

// Supported reports whether a path looks like something we can read.
func Supported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf", ".docx", ".txt", ".md":
		return true
	}
	return false
}

// ReadFile extracts text from one file.
func ReadFile(path string) (Document, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".docx":
		b, err := os.ReadFile(path)
		if err != nil {
			return Document{}, err
		}
		text, err := docxText(b)
		if err != nil {
			return Document{}, err
		}
		return Document{Path: path, Kind: KindDOCX, Text: text}, nil

	case ".pdf":
		b, err := os.ReadFile(path)
		if err != nil {
			return Document{}, err
		}
		text, pages, warn := pdfText(b)
		if strings.TrimSpace(text) == "" {
			return Document{
				Path:  path,
				Kind:  KindScan,
				Pages: pages,
				Warn:  "no text layer: scanned PDF or image-only export, needs OCR",
			}, nil
		}
		return Document{Path: path, Kind: KindPDF, Text: text, Pages: pages, Warn: warn}, nil

	case ".txt", ".md":
		b, err := os.ReadFile(path)
		if err != nil {
			return Document{}, err
		}
		return Document{Path: path, Kind: KindText, Text: string(b)}, nil
	}

	return Document{}, fmt.Errorf("unsupported file type %q", ext)
}

// ReadFolder walks a directory and extracts every supported file.
// Files that fail to parse are returned in the docs slice with a Warn, so one
// bad CV never aborts a bulk import.
func ReadFolder(dir string) ([]Document, error) {
	var paths []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !Supported(p) {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	out := make([]Document, 0, len(paths))
	for _, p := range paths {
		doc, err := ReadFile(p)
		if err != nil {
			out = append(out, Document{Path: p, Kind: "error", Warn: err.Error()})
			continue
		}
		out = append(out, doc)
	}
	return out, nil
}

// ---------------------------------------------------------------- DOCX

// docxText pulls the body text out of a .docx by reading word/document.xml.
// Text nodes are w:t elements; w:tab and w:br become whitespace, and every
// paragraph (w:p) ends a line. That is enough to preserve CV structure, which
// the field parser relies on (name on line 1, sections by line).
func docxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("docx: not a zip archive: %w", err)
	}

	var docFile *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return "", errors.New("docx: word/document.xml not found (not a Word document?)")
	}

	rc, err := docFile.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	var b strings.Builder
	dec := xml.NewDecoder(rc)
	inText := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// A malformed XML chunk should not lose the whole CV.
			if b.Len() > 0 {
				break
			}
			return "", fmt.Errorf("docx: parse document.xml: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteString("\t")
			case "br":
				b.WriteString("\n")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteString("\n")
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
	return normalizeSpace(b.String()), nil
}

// ---------------------------------------------------------------- PDF

var (
	streamRe = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	// Text-showing operators: (...) Tj  and  [ (...) ... ] TJ
	literalRe = regexp.MustCompile(`\((?:\\.|[^\\()])*\)`)
	tjRe      = regexp.MustCompile(`(?s)(\[[^\]]*\]|\((?:[^()\\]|\\.)*\))\s*(Tj|TJ|'|")`)
	// Operators that MOVE the text position. In a PDF a "line break" is a
	// positioning change, not a newline character — without splitting on these,
	// an entire page collapses into one long line and field parsing fails.
	textMoveRe = regexp.MustCompile(`\bT\*|\bTd\b|\bTD\b|\bTm\b|\bTL\b`)
	pageRe     = regexp.MustCompile(`/Type\s*/Page[^s]`)
)

// pdfText extracts visible text from a PDF using only the standard library.
//
// Strategy: find every stream, try FlateDecode, then pull the strings that feed
// Tj/TJ. This handles the common "exported from Word / LibreOffice / Canva"
// case. It does not implement the full PDF text model: no font ToUnicode
// mapping, no CID fonts, no object streams. Those are why the Warn field exists.
func pdfText(data []byte) (text string, pages int, warn string) {
	pages = len(pageRe.FindAll(data, -1))
	if pages == 0 {
		pages = 1
	}

	var out strings.Builder
	streams := streamRe.FindAllSubmatch(data, -1)
	if len(streams) == 0 {
		return "", pages, "no streams found: not a valid PDF"
	}

	decoded := 0
	for _, m := range streams {
		raw := m[1]

		body := raw
		if zr, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
			if inflated, err := io.ReadAll(io.LimitReader(zr, 8<<20)); err == nil {
				body = inflated
				decoded++
			}
			zr.Close()
		}

		// Only streams that contain text operators are worth parsing.
		if !bytes.Contains(body, []byte("Tj")) && !bytes.Contains(body, []byte("TJ")) {
			continue
		}
		out.WriteString(pdfStreamText(body))
		out.WriteString("\n")
	}

	text = normalizeSpace(out.String())
	if text == "" {
		return "", pages, "streams found but no text operators: likely a scanned or vector-outlined PDF"
	}
	if decoded == 0 {
		warn = "no FlateDecode streams: text may be partial"
	}
	return text, pages, warn
}

// pdfStreamText extracts visible text, reconstructing line breaks from the
// text-positioning operators.
//
// A PDF has no newline characters in its text: the renderer moves the cursor
// with Td/TD/T*/Tm/TL and draws a string there. Splitting the content stream on
// those operators and treating each chunk as one line is what makes the output
// usable by the field parser (name on line 1, sections on their own lines).
func pdfStreamText(body []byte) string {
	chunks := textMoveRe.Split(string(body), -1)

	lines := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		var b strings.Builder
		for _, lit := range literalRe.FindAllString(chunk, -1) {
			b.WriteString(unescapePDFString(lit[1 : len(lit)-1]))
			b.WriteString(" ")
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			lines = append(lines, s)
		}
	}
	return strings.Join(lines, "\n")
}

// unescapePDFString resolves the escapes allowed inside a PDF literal string.
func unescapePDFString(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'b', 'f':
			b.WriteByte(' ')
		case '(', ')', '\\':
			b.WriteByte(s[i])
		case '\n':
			// line continuation inside a literal string
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- helpers

var wsRe = regexp.MustCompile(`[ \t\x00-\x08\x0b\x0c\x0e-\x1f]+`)

// normalizeSpace collapses horizontal whitespace but keeps line structure,
// because the field parser uses line position to find the name and sections.
func normalizeSpace(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = wsRe.ReplaceAllString(s, " ")

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			blank++
			if blank > 1 {
				continue // collapse runs of blank lines
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// OCRHint returns the shell command a human would run to OCR a file, so the
// warning is actionable instead of just "failed".
func OCRHint(path string) string {
	return fmt.Sprintf("tesseract %q stdout -l eng+ind", path)
}
