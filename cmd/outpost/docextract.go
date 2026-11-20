package main

// Documents in chat and tasks: PDF, Word (.docx), Excel (.xlsx), OpenDocument
// text (.odt) and CSV/TSV files become plain text the models can read. DOCX,
// XLSX and ODT are zip files of XML and are read with the standard library;
// PDF text comes from github.com/ledongthuc/pdf (BSD-3-Clause, vendored), which
// reads the text layer only: a scanned PDF without one gives no text and a
// warning, and odd font encodings can come out garbled. The text is extracted
// once, on the controller, and kept next to the chat or task.

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

const (
	maxDocBytes     = 30 << 20 // one document as uploaded
	maxDocText      = 400_000  // characters of text kept from one document
	maxDocPages     = 500
	maxXMLPartBytes = 64 << 20 // one unpacked XML part of a DOCX/XLSX/ODT
)

// DocInfo describes what was read from a document; it is shown on the chip.
type DocInfo struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`            // pdf | docx | xlsx | odt | csv | text
	Pages   int    `json:"pages,omitempty"` // PDF pages, or XLSX sheets
	Words   int    `json:"words"`
	Cut     bool   `json:"cut,omitempty"`     // the text was longer than maxDocText
	Warning string `json:"warning,omitempty"` // e.g. no text layer
	File    string `json:"file,omitempty"`    // where the text is kept (chat docs)
}

// docKind returns the kind for a file name, or "" when it is not a document.
func docKind(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf":
		return "pdf"
	case ".docx":
		return "docx"
	case ".xlsx", ".xlsm":
		return "xlsx"
	case ".odt":
		return "odt"
	case ".csv", ".tsv":
		return "csv"
	}
	return ""
}

var pdfPageMark = regexp.MustCompile(`\[page \d+\]`)

// docMarks are the [page N] and [sheet X] lines the readers add; they are not counted as words.
var docMarks = regexp.MustCompile(`(?m)^\[(?:page \d+|sheet [^\]\n]*)\]$`)

var errNotDoc = errors.New("not a PDF, DOCX, XLSX, ODT or CSV file")

// extractDocument returns the document's text and what was read.
func extractDocument(name string, data []byte) (text string, info DocInfo, err error) {
	info = DocInfo{Name: name, Kind: docKind(name)}
	if len(data) > maxDocBytes {
		return "", info, fmt.Errorf("%s is larger than %d MB", name, maxDocBytes>>20)
	}
	defer func() { // the parsers are given untrusted files; a panic is a bad file, not a crash
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("%s could not be read (%v)", name, r)
		}
	}()
	switch info.Kind {
	case "pdf":
		text, info.Pages, err = pdfText(data)
		if err == nil && len(strings.Fields(pdfPageMark.ReplaceAllString(text, ""))) < max(3, info.Pages) {
			info.Warning = "This PDF has little or no text layer (a scan?); only the text it has was read. Pages are not read as images."
		}
	case "docx":
		text, err = docxText(data)
	case "odt":
		text, err = odtText(data)
	case "xlsx":
		text, info.Pages, err = xlsxText(data)
	case "csv":
		text, err = csvText(name, data)
	default:
		err = errNotDoc
	}
	if err != nil {
		return "", info, err
	}
	text = strings.ToValidUTF8(text, "")
	text = regexp.MustCompile(`[ \t]+\n`).ReplaceAllString(text, "\n")
	text = strings.TrimSpace(regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n"))
	if len(text) > maxDocText {
		text, info.Cut = strings.ToValidUTF8(text[:maxDocText], "")+"\n…(cut: the document is longer)", true
	}
	info.Words = len(strings.Fields(docMarks.ReplaceAllString(text, "")))
	return text, info, nil
}

// docBlock wraps a document's text for a prompt.
func docBlock(info DocInfo, text string) string {
	head := fmt.Sprintf("Attached document %s (%s", info.Name, strings.ToUpper(info.Kind))
	if info.Kind == "pdf" && info.Pages > 0 {
		head += fmt.Sprintf(", %d pages; page breaks are marked [page N]", info.Pages)
	}
	if info.Kind == "xlsx" && info.Pages > 0 {
		head += fmt.Sprintf(", %d sheets", info.Pages)
	}
	return head + "):\n<<<DOCUMENT>>>\n" + text + "\n<<<END DOCUMENT>>>"
}

// ---- PDF ----

func pdfText(data []byte) (string, int, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "encrypt") || strings.Contains(err.Error(), "password") {
			return "", 0, errors.New("the PDF is password-protected")
		}
		return "", 0, fmt.Errorf("not a readable PDF: %v", err)
	}
	n := r.NumPage()
	var b strings.Builder
	for i := 1; i <= n && i <= maxDocPages && b.Len() < maxDocText; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		fmt.Fprintf(&b, "\n[page %d]\n", i)
		rows, err := p.GetTextByRow()
		if err != nil {
			continue
		}
		for _, row := range rows {
			var line strings.Builder
			lastX := -1.0
			for _, w := range row.Content {
				if lastX >= 0 && w.X-lastX > w.FontSize*0.15 && !strings.HasPrefix(w.S, " ") && !strings.HasSuffix(line.String(), " ") {
					line.WriteByte(' ')
				}
				line.WriteString(w.S)
				lastX = w.X + w.W
			}
			if s := strings.TrimSpace(line.String()); s != "" {
				b.WriteString(s + "\n")
			}
		}
	}
	if n > maxDocPages {
		fmt.Fprintf(&b, "\n…(only the first %d of %d pages were read)\n", maxDocPages, n)
	}
	return b.String(), n, nil
}

// ---- zip + XML (DOCX, ODT, XLSX) ----

func zipPart(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			if f.UncompressedSize64 > maxXMLPartBytes {
				return nil, fmt.Errorf("%s is too large", name)
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxXMLPartBytes))
		}
	}
	return nil, fmt.Errorf("%s is missing", name)
}

func openZip(data []byte, what string) (*zip.Reader, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a valid %s file", what)
	}
	return zr, nil
}

// xmlText walks an XML part: text inside elements named by isText is kept,
// elements in breaks end a line, and tab adds a tab.
func xmlText(data []byte, isText func(xml.Name) bool, breaks map[string]bool, tab map[string]bool) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var b strings.Builder
	depth := 0
	for b.Len() < maxDocText {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return b.String(), nil // keep what was read
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if isText(t.Name) {
				depth++
			}
			if tab[t.Name.Local] {
				b.WriteByte('\t')
			}
			if t.Name.Local == "br" {
				b.WriteByte('\n')
			}
		case xml.EndElement:
			if isText(t.Name) && depth > 0 {
				depth--
			}
			if breaks[t.Name.Local] {
				b.WriteByte('\n')
			}
		case xml.CharData:
			if depth > 0 {
				b.Write(t)
			}
		}
	}
	return b.String(), nil
}

func docxText(data []byte) (string, error) {
	zr, err := openZip(data, "Word (.docx)")
	if err != nil {
		return "", err
	}
	part, err := zipPart(zr, "word/document.xml")
	if err != nil {
		return "", errors.New("not a Word document (word/document.xml is missing)")
	}
	return xmlText(part, func(n xml.Name) bool { return n.Local == "t" }, map[string]bool{"p": true, "tr": true}, map[string]bool{"tab": true, "tc": true})
}

func odtText(data []byte) (string, error) {
	zr, err := openZip(data, "OpenDocument (.odt)")
	if err != nil {
		return "", err
	}
	part, err := zipPart(zr, "content.xml")
	if err != nil {
		return "", errors.New("not an OpenDocument text (content.xml is missing)")
	}
	inBody := func(n xml.Name) bool { return n.Local == "p" || n.Local == "h" || n.Local == "span" }
	return xmlText(part, inBody, map[string]bool{"p": true, "h": true}, map[string]bool{"tab": true})
}

type xlsxSheet struct {
	Name string `xml:"name,attr"`
	RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
}

func xlsxText(data []byte) (string, int, error) {
	zr, err := openZip(data, "Excel (.xlsx)")
	if err != nil {
		return "", 0, err
	}
	// Shared strings: most text cells point into this list.
	var shared []string
	if part, err := zipPart(zr, "xl/sharedStrings.xml"); err == nil {
		dec := xml.NewDecoder(bytes.NewReader(part))
		var cur strings.Builder
		in, inT := false, false
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "si" {
					in = true
					cur.Reset()
				} else if t.Name.Local == "t" {
					inT = true
				}
			case xml.EndElement:
				if t.Name.Local == "si" {
					shared = append(shared, cur.String())
					in = false
				} else if t.Name.Local == "t" {
					inT = false
				}
			case xml.CharData:
				if in && inT {
					cur.Write(t)
				}
			}
		}
	}
	var wb struct {
		Sheets []xlsxSheet `xml:"sheets>sheet"`
	}
	if part, err := zipPart(zr, "xl/workbook.xml"); err == nil {
		xml.Unmarshal(part, &wb)
	}
	rels := map[string]string{}
	if part, err := zipPart(zr, "xl/_rels/workbook.xml.rels"); err == nil {
		var r struct {
			Rel []struct {
				ID     string `xml:"Id,attr"`
				Target string `xml:"Target,attr"`
			} `xml:"Relationship"`
		}
		xml.Unmarshal(part, &r)
		for _, x := range r.Rel {
			t := strings.TrimPrefix(x.Target, "/")
			if !strings.HasPrefix(t, "xl/") {
				t = "xl/" + t
			}
			rels[x.ID] = t
		}
	}
	type sheetRef struct{ name, file string }
	var sheets []sheetRef
	for i, s := range wb.Sheets {
		f := rels[s.RID]
		if f == "" {
			f = fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)
		}
		sheets = append(sheets, sheetRef{s.Name, f})
	}
	if len(sheets) == 0 {
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
				sheets = append(sheets, sheetRef{strings.TrimSuffix(path.Base(f.Name), ".xml"), f.Name})
			}
		}
		sort.Slice(sheets, func(i, j int) bool { return sheets[i].file < sheets[j].file })
	}
	if len(sheets) == 0 {
		return "", 0, errors.New("the workbook has no sheets")
	}
	var b strings.Builder
	for _, sh := range sheets {
		part, err := zipPart(zr, sh.file)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "\n[sheet %s]\n", sh.name)
		var ws struct {
			Rows []struct {
				Cells []struct {
					Ref string `xml:"r,attr"`
					T   string `xml:"t,attr"`
					V   string `xml:"v"`
					IS  struct {
						T []string `xml:"t"`
						R []struct {
							T string `xml:"t"`
						} `xml:"r"`
					} `xml:"is"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		if xml.Unmarshal(part, &ws) != nil {
			continue
		}
		for _, row := range ws.Rows {
			var cells []string
			col := 0
			for _, c := range row.Cells {
				if want := xlsxCol(c.Ref); want > col { // keep empty cells, so columns line up
					for ; col < want && col < 200; col++ {
						cells = append(cells, "")
					}
				}
				v := c.V
				switch c.T {
				case "s":
					if i, err := strconv.Atoi(c.V); err == nil && i >= 0 && i < len(shared) {
						v = shared[i]
					}
				case "inlineStr":
					v = strings.Join(c.IS.T, "")
					for _, r := range c.IS.R {
						v += r.T
					}
				case "b":
					v = map[string]string{"1": "TRUE", "0": "FALSE"}[c.V]
				}
				cells = append(cells, strings.ReplaceAll(strings.ReplaceAll(v, "\t", " "), "\n", " "))
				col++
			}
			for len(cells) > 0 && cells[len(cells)-1] == "" {
				cells = cells[:len(cells)-1]
			}
			if len(cells) > 0 {
				b.WriteString(strings.Join(cells, "\t") + "\n")
			}
			if b.Len() > maxDocText {
				break
			}
		}
	}
	return b.String(), len(sheets), nil
}

// xlsxCol turns a cell reference like "C7" into a 0-based column (2).
func xlsxCol(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	return n - 1
}

// ---- CSV ----

func csvText(name string, data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", errors.New("the file is not UTF-8 text")
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	if strings.HasSuffix(strings.ToLower(name), ".tsv") {
		r.Comma = '\t'
	}
	var b strings.Builder
	for b.Len() < maxDocText {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return string(data), nil // not well-formed: hand over the text as it is
		}
		b.WriteString(strings.Join(rec, "\t") + "\n")
	}
	return b.String(), nil
}
