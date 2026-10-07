package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// Minimal workbook parts; xlsxParts fills in the rest.
const (
	bombContentTypes = `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`
	bombRootRels     = `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`
	bombWorkbook     = `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`
	bombWorkbookRels = `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/></Relationships>`
	bombSheet        = `<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c></row><row r="2"><c r="A2" t="s"><v>1</v></c></row></sheetData></worksheet>`
	bombSSTHead      = `<?xml version="1.0" encoding="UTF-8"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>Иванов Иван</t></si><si><t>Петров Пётр</t></si>`
)

// part is one zip entry; body is written repeat times (cheap huge parts).
type part struct {
	name   string
	body   string
	repeat int
	tail   string
}

// xlsxParts builds a workbook zip from parts (later parts with the same name
// as a default replace it).
func xlsxParts(t *testing.T, extra ...part) []byte {
	t.Helper()
	parts := []part{
		{name: "[Content_Types].xml", body: bombContentTypes},
		{name: "_rels/.rels", body: bombRootRels},
		{name: "xl/workbook.xml", body: bombWorkbook},
		{name: "xl/_rels/workbook.xml.rels", body: bombWorkbookRels},
		{name: "xl/worksheets/sheet1.xml", body: bombSheet},
		{name: "xl/sharedStrings.xml", body: bombSSTHead, tail: `</sst>`},
	}
	for _, e := range extra {
		replaced := false
		for i := range parts {
			if parts[i].name == e.name {
				parts[i], replaced = e, true
			}
		}
		if !replaced {
			parts = append(parts, e)
		}
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			t.Fatal(err)
		}
		n := max(p.repeat, 1)
		chunk := strings.Repeat(p.body, max(1, min(n, 1<<16/max(len(p.body), 1))))
		per := len(chunk) / max(len(p.body), 1)
		for done := 0; done < n; done += per {
			k := min(per, n-done)
			if _, err := io.WriteString(w, chunk[:k*len(p.body)]); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := io.WriteString(w, p.tail); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseXLSXBombs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		parts []part
		kind  error
	}{
		{"13 million empty shared strings (65 MB of XML in a ~100 KB upload)",
			[]part{{name: "xl/sharedStrings.xml", body: "<si/>", repeat: 13_000_000, tail: "</sst>"}}, ErrTooLarge},
		{"shared strings with too many entries in a small part",
			[]part{{name: "xl/sharedStrings.xml", body: "<si/>", repeat: xlsxMaxSharedStringsElements + 10, tail: "</sst>"}}, ErrTooLarge},
		{"shared strings above the part size limit",
			[]part{{name: "xl/sharedStrings.xml", body: "<si><t>" + strings.Repeat("Иванов ", 1000) + "</t></si>", repeat: 400, tail: "</sst>"}}, ErrTooLarge},
		{"shared strings under another spelling of the name",
			[]part{{name: `XL\SharedStrings.XML`, body: "<si/>", repeat: xlsxMaxSharedStringsElements + 10, tail: "</sst>"}}, ErrTooLarge},
		{"styles with too many formats",
			[]part{{name: "xl/styles.xml", body: "<xf/>", repeat: xlsxMaxPartElements + 10, tail: "</cellXfs></styleSheet>"}}, ErrTooLarge},
		{"a workbook hidden among the worksheets",
			[]part{{name: "xl/worksheets/workbook.xml", body: `<sheet name="x" sheetId="1"/>`, repeat: xlsxMaxPartElements + 10}}, ErrTooLarge},
		{"parts adding up to too many elements",
			[]part{
				{name: "xl/styles.xml", body: "<xf/>", repeat: xlsxMaxPartElements - 10},
				{name: "xl/theme/theme1.xml", body: "<a/>", repeat: xlsxMaxPartElements - 10},
				{name: "docProps/custom.xml", body: "<p/>", repeat: xlsxMaxPartElements - 10},
			}, ErrTooLarge},
		{"too many zip entries", manyEntries(xlsxMaxEntries + 1), ErrTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := xlsxParts(t, tt.parts...)
			start := time.Now()
			_, _, err := Parse("a.xlsx", data)
			if !errors.Is(err, tt.kind) {
				t.Fatalf("Parse error = %v, want %v (upload %d bytes)", err, tt.kind, len(data))
			}
			if !containsCyrillic(err.Error()) {
				t.Errorf("error %q is not a Russian user message", err)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("rejecting took %v", d)
			}
		})
	}
}

func manyEntries(n int) []part {
	out := make([]part, n)
	for i := range out {
		out[i] = part{name: fmt.Sprintf("xl/media/x%d.bin", i), body: "x"}
	}
	return out
}

func TestParseXLSXBrokenArchive(t *testing.T) {
	t.Parallel()
	data := xlsxParts(t)
	_, _, err := Parse("a.xlsx", data[:len(data)/2])
	if !errors.Is(err, ErrUnreadable) {
		t.Fatalf("Parse(truncated) = %v, want ErrUnreadable", err)
	}
}

// Real lists still import: the checks only stop pathological workbooks.
func TestParseXLSXLargeRealList(t *testing.T) {
	t.Parallel()
	data := buildXLSX(t, func(f *excelize.File) {
		must(t, f.SetSheetName("Sheet1", "Студенты"))
		_, err := f.NewSheet("Справочник")
		must(t, err)
		sw, err := f.NewStreamWriter("Студенты")
		must(t, err)
		must(t, sw.SetRow("A1", []any{"Табельный номер", "ФИО", "Группа", "Почта"}))
		for i := range MaxRows {
			cell, _ := excelize.CoordinatesToCellName(1, i+2)
			must(t, sw.SetRow(cell, []any{fmt.Sprintf("%06d", i+1), fmt.Sprintf("Студентов-%d Студент Студентович", i),
				"Группа ДО-1", fmt.Sprintf("student%d@example.com", i)}))
		}
		must(t, sw.Flush())
		for i := range 200 {
			must(t, f.SetCellValue("Справочник", fmt.Sprintf("A%d", i+1), fmt.Sprintf("Справка %d", i)))
		}
	})
	rows, det, err := Parse("Группа.xlsx", data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(rows) != MaxRows || det.NameColumn != "ФИО" || rows[0].FullName != "Студентов-0 Студент Студентович" || rows[0].ExternalID != "000001" {
		t.Fatalf("rows=%d det=%+v first=%+v", len(rows), det, rows[0])
	}
}

func TestParseContextBusy(t *testing.T) {
	t.Parallel()
	data := xlsxParts(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Hold the slot so the cancelled caller has to wait for it.
	xlsxSlot <- struct{}{}
	_, _, err := ParseContext(ctx, "a.xlsx", data)
	<-xlsxSlot
	if !errors.Is(err, ErrBusy) || !containsCyrillic(err.Error()) {
		t.Fatalf("ParseContext(cancelled) = %v, want ErrBusy with a Russian message", err)
	}
	rows, _, err := ParseContext(context.Background(), "a.xlsx", data)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ParseContext = %v rows, %v", len(rows), err)
	}
}
