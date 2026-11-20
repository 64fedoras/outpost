package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// testPDF builds a small valid PDF with one line of text per page.
func testPDF(pages ...string) []byte {
	var b bytes.Buffer
	var offs []int
	obj := func(s string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), s)
	}
	b.WriteString("%PDF-1.4\n")
	n := len(pages)
	kids := []string{}
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", 4+2*i))
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	for i, text := range pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+2*i))
		content := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return b.Bytes()
}

func testZip(files map[string]string) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	return b.Bytes()
}

func TestExtractDocuments(t *testing.T) {
	text, info, err := extractDocument("report.pdf", testPDF("Quarterly revenue grew 12 percent", "Costs fell in March"))
	if err != nil || info.Pages != 2 || !strings.Contains(text, "[page 2]") || !strings.Contains(text, "Costs fell in March") || !strings.Contains(text, "revenue grew") {
		t.Fatalf("pdf: %v %+v %q", err, info, text)
	}
	if info.Warning != "" || info.Words < 8 {
		t.Fatalf("pdf info %+v", info)
	}
	if _, _, err := extractDocument("bad.pdf", []byte("%PDF-1.4 garbage")); err == nil {
		t.Fatal("a broken PDF must be an error, not a panic")
	}
	docx := testZip(map[string]string{"word/document.xml": `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
		<w:p><w:r><w:t>Invoice terms</w:t></w:r></w:p><w:p><w:r><w:t xml:space="preserve">Pay within </w:t></w:r><w:r><w:t>30 days.</w:t></w:r></w:p>
		<w:tbl><w:tr><w:tc><w:p><w:r><w:t>A</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>B</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`})
	text, info, err = extractDocument("terms.docx", docx)
	if err != nil || !strings.Contains(text, "Invoice terms\nPay within 30 days.") || !strings.Contains(text, "A") || info.Kind != "docx" {
		t.Fatalf("docx: %v %q", err, text)
	}
	xlsx := testZip(map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Budget" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>Item</t></si><si><t>Cost</t></si><si><r><t>Ser</t></r><r><t>ver</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="s"><v>2</v></c><c r="C2"><v>1200.5</v></c></row></sheetData></worksheet>`,
	})
	text, info, err = extractDocument("budget.xlsx", xlsx)
	if err != nil || info.Pages != 1 || !strings.Contains(text, "[sheet Budget]\nItem\tCost\nServer\t\t1200.5") {
		t.Fatalf("xlsx: %v %+v %q", err, info, text)
	}
	odt := testZip(map[string]string{"content.xml": `<office:document-content xmlns:office="o" xmlns:text="t"><office:body><office:text><text:h>Notes</text:h><text:p>First <text:span>point</text:span>.</text:p></office:text></office:body></office:document-content>`})
	if text, _, err = extractDocument("notes.odt", odt); err != nil || !strings.Contains(text, "Notes\nFirst point.") {
		t.Fatalf("odt: %v %q", err, text)
	}
	if text, _, err = extractDocument("data.csv", []byte("a,b\n\"x, y\",2\n")); err != nil || text != "a\tb\nx, y\t2" {
		t.Fatalf("csv: %v %q", err, text)
	}
	if _, _, err := extractDocument("x.docx", []byte("not a zip")); err == nil {
		t.Fatal("bad docx")
	}
	if docKind("a.PDF") != "pdf" || docKind("a.txt") != "" {
		t.Fatal("docKind")
	}
	// A PDF without a text layer: a warning, not an error.
	if _, info, err := extractDocument("scan.pdf", testPDF("", "")); err != nil || info.Warning == "" {
		t.Fatalf("scan: %v %+v", err, info)
	}
}

// A PDF attached to a task is read into text for the planner.
func TestTaskDocAttachment(t *testing.T) {
	c := testController(t)
	task := &Task{ID: "task_d", dir: t.TempDir()}
	err := c.applyAttachments(task, []attachedFile{
		{Name: "spec.pdf", Data: testPDF("The API must use port 8080", "Second page text")},
		{Name: "notes.txt", Data: []byte("keep it small")},
	})
	if err != nil || len(task.Attachments) != 2 {
		t.Fatalf("err=%v att=%+v", err, task.Attachments)
	}
	a := task.Attachments[0]
	if a.Kind != "doc" || a.Pages != 2 || a.Words == 0 {
		t.Fatalf("doc attachment = %+v", a)
	}
	ctx := attachmentContext(task, 20000)
	for _, want := range []string{"spec.pdf", "port 8080", "[page 2]", "keep it small"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("context lacks %q:\n%s", want, ctx)
		}
	}
	if err := c.applyAttachments(&Task{dir: t.TempDir()}, []attachedFile{{Name: "bad.pdf", Data: []byte("not a pdf")}}); err == nil {
		t.Fatal("broken PDF accepted")
	}
}
