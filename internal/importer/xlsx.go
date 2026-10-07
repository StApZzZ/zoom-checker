package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Zip-bomb guards: a 10 MiB upload may not expand beyond these sizes. The XML
// limit stays above xlsxMaxPartSize, so excelize never takes its temp-file
// path for shared strings (whose index also grows with the entry count).
const (
	xlsxUnzipLimit    = 32 << 20
	xlsxUnzipXMLLimit = 16 << 20
	// Extra non-blank rows allowed above the data (titles, header).
	xlsxRowSlack = 20
)

// Limits checked on the zip directory and the decompressed parts before
// excelize sees the file (checkXLSXArchive). excelize decodes whole parts
// such as the shared strings, styles and workbook into Go structs, and every
// XML element costs up to ~450 bytes there: a 100 KB upload of 13 million
// "<si/>" would otherwise allocate gigabytes. Worksheets are streamed row by
// row and only count against the size limits.
const (
	xlsxMaxEntries = 5000
	// xlsxMaxPartSize bounds each part decoded as a whole (everything that is
	// not a worksheet).
	xlsxMaxPartSize = 4 << 20
	// xlsxMaxPartElements bounds the elements of one such part; the shared
	// strings get more (two or more elements per text cell). 10,000 names
	// need ~20,000.
	xlsxMaxPartElements          = 100_000
	xlsxMaxSharedStringsElements = 200_000
	// xlsxMaxElements bounds all of them together.
	xlsxMaxElements = 200_000
)

const msgXLSXTooComplex = "Таблица слишком большая или сложная, чтобы её загрузить. Скопируйте список студентов " +
	"на новый лист новой книги Excel и загрузите её — или вставьте список текстом."

var errSheetTooLong = errors.New("importer: sheet has too many rows")

// xlsxSlot lets one workbook be parsed at a time: a parse can take tens of
// megabytes and a second of CPU, parallel uploads must not stack that up.
var xlsxSlot = make(chan struct{}, 1)

// parseXLSX reads the first non-empty worksheet (visible sheets first) of a
// workbook. Cell values are taken as Excel displays them, so IDs keep leading
// zeros and long numbers are not turned into 1.2E+13.
func parseXLSX(ctx context.Context, data []byte) ([]Row, Detected, error) {
	det := Detected{Format: FormatXLSX, Encoding: EncodingUTF8}
	if err := checkXLSXArchive(data); err != nil {
		return nil, det, err
	}
	select {
	case xlsxSlot <- struct{}{}:
	case <-ctx.Done():
		return nil, det, fileErr(ErrBusy, msgBusy, ctx.Err())
	}
	defer func() { <-xlsxSlot }()
	f, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{
		UnzipSizeLimit:    xlsxUnzipLimit,
		UnzipXMLSizeLimit: xlsxUnzipXMLLimit,
	})
	if err != nil {
		return nil, det, fileErr(ErrUnreadable, msgBadXLSX, err)
	}
	defer func() { _ = f.Close() }()

	for _, sheet := range sheetOrder(f) {
		recs, err := readSheet(f, sheet)
		if errors.Is(err, errSheetTooLong) {
			return nil, det, tooManyRows()
		}
		if err != nil {
			return nil, det, fileErr(ErrUnreadable, msgBadXLSX, err)
		}
		if len(recs) == 0 {
			continue
		}
		rows, d := parseTable(table{recs: recs})
		det.NameColumn, det.HasHeader = d.NameColumn, d.HasHeader
		if len(rows) == 0 {
			return nil, det, fileErr(ErrEmptyFile, msgEmpty, nil)
		}
		if len(rows) > MaxRows {
			return nil, det, tooManyRows()
		}
		return rows, det, nil
	}
	return nil, det, fileErr(ErrEmptyFile, msgEmpty, nil)
}

// sheetOrder lists visible sheets first, then hidden ones, each in workbook order.
func sheetOrder(f *excelize.File) []string {
	var visible, hidden []string
	for _, s := range f.GetSheetList() {
		if ok, err := f.GetSheetVisible(s); err == nil && !ok {
			hidden = append(hidden, s)
		} else {
			visible = append(visible, s)
		}
	}
	return append(visible, hidden...)
}

// readSheet streams the non-blank rows of a sheet; record.line is the Excel
// row number.
func readSheet(f *excelize.File, sheet string) ([]record, error) {
	rows, err := f.Rows(sheet)
	if err != nil {
		return nil, fmt.Errorf("open sheet %q: %w", sheet, err)
	}
	defer func() { _ = rows.Close() }()
	var recs []record
	// Rows.Next advances one row number at a time, also over rows missing
	// from the XML, so the counter equals the Excel row number.
	for line := 1; rows.Next(); line++ {
		cols, err := rows.Columns()
		if err != nil {
			return nil, fmt.Errorf("read sheet %q row %d: %w", sheet, line, err)
		}
		if blank(cols) {
			continue
		}
		if len(recs) >= MaxRows+xlsxRowSlack {
			return nil, errSheetTooLong
		}
		recs = append(recs, record{line: line, fields: cols})
	}
	if err := rows.Error(); err != nil {
		return nil, fmt.Errorf("read sheet %q: %w", sheet, err)
	}
	return recs, nil
}

// checkXLSXArchive rejects workbooks that would make excelize allocate far
// more than the upload (see xlsxMaxElements) before anything is parsed. It
// reads the zip directory and streams every part once; archive/zip fails a
// part that inflates past the size its header declares, so the declared sizes
// can be trusted. Errors are *FileError: ErrTooLarge for oversized or overly
// complex workbooks, ErrUnreadable for broken archives.
func checkXLSXArchive(data []byte) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fileErr(ErrUnreadable, msgBadXLSX, err)
	}
	if len(zr.File) > xlsxMaxEntries {
		return fileErr(ErrTooLarge, msgXLSXTooComplex, fmt.Errorf("%d zip entries", len(zr.File)))
	}
	var total uint64
	for _, f := range zr.File {
		total += f.UncompressedSize64
	}
	if total > xlsxUnzipLimit {
		return fileErr(ErrTooLarge, msgXLSXTooComplex, fmt.Errorf("%d bytes uncompressed", total))
	}
	elements := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		info, err := scanXLSXPart(f)
		if err != nil {
			return fileErr(ErrUnreadable, msgBadXLSX, fmt.Errorf("%s: %w", f.Name, err))
		}
		// A worksheet is streamed row by row; any other part (whatever its
		// name: the workbook path comes from the package relationships) may
		// be decoded as a whole.
		if info.root == "worksheet" {
			continue
		}
		limit := xlsxMaxPartElements
		if strings.EqualFold(strings.ReplaceAll(f.Name, "\\", "/"), "xl/sharedStrings.xml") {
			limit = xlsxMaxSharedStringsElements
		}
		elements += info.elements
		if f.UncompressedSize64 > xlsxMaxPartSize || info.elements > limit || elements > xlsxMaxElements {
			return fileErr(ErrTooLarge, msgXLSXTooComplex,
				fmt.Errorf("%s: %d bytes, %d elements (%d in total)", f.Name, f.UncompressedSize64, info.elements, elements))
		}
	}
	return nil
}

// xlsxPartInfo is what scanXLSXPart learns about one zip entry.
type xlsxPartInfo struct {
	root     string // local name of the first element ("" for non-XML data)
	elements int    // element start tags: '<' followed by anything but / ? !
}

// scanXLSXPart streams one entry and counts its element start tags. The
// count is a plain byte scan (no XML parsing), so it costs almost nothing and
// cannot be fooled by the namespace prefixes strict OOXML uses.
func scanXLSXPart(f *zip.File) (xlsxPartInfo, error) {
	rc, err := f.Open()
	if err != nil {
		return xlsxPartInfo{}, err
	}
	defer func() { _ = rc.Close() }()
	var (
		info    xlsxPartInfo
		afterLT bool // the previous byte was '<'
		inRoot  bool // collecting the root element's name
		name    []byte
		buf     = make([]byte, 32<<10)
	)
	for {
		n, err := rc.Read(buf)
		for _, c := range buf[:n] {
			switch {
			case afterLT:
				afterLT = false
				if c != '/' && c != '?' && c != '!' {
					info.elements++
					if info.elements == 1 {
						inRoot, name = true, append(name[:0], c)
					}
				}
			case inRoot:
				if c == ' ' || c == '>' || c == '/' || c == '\t' || c == '\r' || c == '\n' || len(name) >= 64 {
					inRoot = false
					root := string(name)
					if i := strings.LastIndexByte(root, ':'); i >= 0 {
						root = root[i+1:]
					}
					info.root = root
				} else {
					name = append(name, c)
				}
			}
			if c == '<' {
				afterLT = true
			}
		}
		if errors.Is(err, io.EOF) {
			return info, nil
		}
		if err != nil {
			return info, err
		}
	}
}
