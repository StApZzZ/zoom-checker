// Package importer parses student lists (CSV, XLSX, plain or pasted text) and
// validates them against the current roster of a group (TZ §8, §25).
//
// The parser is deliberately forgiving because lists come from non-technical
// users: it detects encodings (UTF-8, UTF-16 "Unicode text" from Excel,
// windows-1251), delimiters, Russian/English headers, separate
// Фамилия/Имя/Отчество columns, numbered and bulleted lists. Validation never
// does fuzzy matching: names are compared only after names.Normalize.
package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// Limits applied to uploaded lists.
const (
	// MaxRows is the maximum number of student rows in one import.
	MaxRows = 10000
	// MaxFileSize is the maximum accepted file size in bytes.
	MaxFileSize = 10 << 20
	// MaxNameLength is the maximum full name length in characters (runes).
	MaxNameLength = 200
	// MaxExternalIDLength is the maximum external ID length in characters (runes).
	MaxExternalIDLength = 100
)

// Detected formats.
const (
	FormatCSV  = "csv"
	FormatXLSX = "xlsx"
	FormatText = "text"
)

// Detected encodings.
const (
	EncodingUTF8        = "utf-8"
	EncodingUTF16LE     = "utf-16le"
	EncodingUTF16BE     = "utf-16be"
	EncodingWindows1251 = "windows-1251"
)

// Row is one student line of an imported list.
type Row struct {
	Line       int    // 1-based line (text) or row (XLSX) number in the source
	ExternalID string // optional ID from the source system; trimmed
	FullName   string // trimmed, inner whitespace collapsed, letter case kept
}

// Detected describes how the input was understood; it is shown to the user in
// the import preview so they can spot a wrong guess.
type Detected struct {
	Format     string // FormatCSV | FormatXLSX | FormatText
	Encoding   string // EncodingXxx
	Delimiter  string // "," ";" "\t" "|" or "" (single column / XLSX)
	NameColumn string // header text of the name column(s), or "Колонка N" without a header
	HasHeader  bool
}

// Sentinel errors; use errors.Is on the error returned by Parse.
var (
	ErrEmptyFile         = errors.New("importer: no student rows in file")
	ErrUnreadable        = errors.New("importer: file is unreadable")
	ErrUnsupportedFormat = errors.New("importer: unsupported file format")
	ErrTooManyRows       = errors.New("importer: too many rows")
	ErrTooLarge          = errors.New("importer: file too large")
	// ErrBusy: the caller gave up waiting for its turn to parse a workbook.
	ErrBusy = errors.New("importer: busy")
)

// FileError is returned by Parse when a file cannot be used. Error() returns a
// friendly Russian message that can be shown to the user as is; the technical
// cause (if any) is available via errors.Unwrap / errors.As for logging.
type FileError struct {
	Kind    error  // one of the ErrXxx sentinels
	Message string // Russian, for the end user
	Err     error  // technical cause, may be nil
}

func (e *FileError) Error() string { return e.Message }

// Unwrap exposes both the sentinel and the cause to errors.Is / errors.As.
func (e *FileError) Unwrap() []error {
	errs := []error{e.Kind}
	if e.Err != nil {
		errs = append(errs, e.Err)
	}
	return errs
}

// UserMessage returns the Russian message to show for an error returned by
// Parse, or a generic one for any other error.
func UserMessage(err error) string {
	var fe *FileError
	if errors.As(err, &fe) {
		return fe.Message
	}
	return msgUnreadable
}

const (
	msgEmpty        = "В файле не нашлось ни одного студента. Проверьте, что список не пустой и имена записаны в первом листе или первых колонках."
	msgUnreadable   = "Не удалось прочитать файл. Сохраните список в Excel как «Книга Excel (.xlsx)» или как CSV и попробуйте ещё раз."
	msgBadXLSX      = "Не удалось открыть файл как таблицу Excel. Возможно, он повреждён или это файл другого типа. Сохраните список в Excel как «Книга Excel (.xlsx)» или как CSV."
	msgOLE          = "Файл в старом формате Excel (.xls) или защищён паролем. Откройте его в Excel и сохраните как «Книга Excel (.xlsx)» без пароля или как CSV."
	msgPDF          = "Файлы PDF не поддерживаются. Загрузите список в Excel (.xlsx), CSV или вставьте имена текстом."
	msgBinary       = "Это не похоже на таблицу или текстовый файл. Загрузите список в Excel (.xlsx), CSV или вставьте имена текстом."
	msgTooLarge     = "Файл слишком большой (больше 10 МБ). Оставьте в нём только список студентов."
	msgWord         = "Документы Word не поддерживаются. Скопируйте список из документа и вставьте его текстом или сохраните таблицу в Excel как «Книга Excel (.xlsx)»."
	msgEmptyText    = "В тексте не нашлось ни одного студента. Вставьте список: по одному студенту в строке."
	msgTextTooLarge = "Текст слишком большой (больше 10 МБ). Оставьте в нём только список студентов."
	msgBusy         = "Сервер сейчас обрабатывает другой список. Попробуйте ещё раз через минуту."
	msgMarkup       = "Файл похож на веб-страницу, а не на таблицу (так бывает, когда программа выгружает «Excel» в виде HTML). Откройте его в Excel и сохраните как «Книга Excel (.xlsx)» или как CSV."
)

func fileErr(kind error, msg string, cause error) error {
	return &FileError{Kind: kind, Message: msg, Err: cause}
}

var (
	zipMagic = []byte("PK\x03\x04")
	oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
	pdfMagic = []byte("%PDF")
)

// Parse reads an uploaded student list. XLSX is recognized by the zip magic
// bytes or the .xlsx/.xlsm extension; everything else is treated as text
// (CSV, TSV, one name per line). The returned error is a *FileError.
func Parse(filename string, data []byte) ([]Row, Detected, error) {
	return ParseContext(context.Background(), filename, data)
}

// ParseContext is Parse for request handlers: workbooks are parsed one at a
// time, and a caller whose ctx ends while it waits for its turn gets ErrBusy.
func ParseContext(ctx context.Context, filename string, data []byte) ([]Row, Detected, error) {
	if len(data) > MaxFileSize {
		return nil, Detected{}, fileErr(ErrTooLarge, msgTooLarge, nil)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, Detected{}, fileErr(ErrEmptyFile, msgEmpty, nil)
	}
	ext := strings.ToLower(filepath.Ext(filename))
	isZip := bytes.HasPrefix(data, zipMagic)
	switch {
	case (isZip || bytes.HasPrefix(data, oleMagic)) && (ext == ".doc" || ext == ".docx" || ext == ".odt"):
		return nil, Detected{}, fileErr(ErrUnsupportedFormat, msgWord, nil)
	case bytes.HasPrefix(data, oleMagic):
		return nil, Detected{}, fileErr(ErrUnsupportedFormat, msgOLE, nil)
	case bytes.HasPrefix(data, pdfMagic):
		return nil, Detected{}, fileErr(ErrUnsupportedFormat, msgPDF, nil)
	case isZip || ext == ".xlsx" || ext == ".xlsm":
		return parseXLSX(ctx, data)
	}

	text, enc, err := decodeText(data)
	if err != nil {
		return nil, Detected{}, err
	}
	// Reject an over-long list before splitting it into cells: a 10 MB file
	// of short lines would otherwise allocate gigabytes on the way.
	if nonBlankLines(text) > 2*(MaxRows+headerSearchRows) {
		return nil, Detected{Format: FormatCSV, Encoding: enc}, tooManyRows()
	}
	rows, det := parseTable(splitText(text))
	det.Encoding = enc
	det.Format = FormatText
	if det.Delimiter != "" || ext == ".csv" || ext == ".tsv" {
		det.Format = FormatCSV
	}
	if len(rows) == 0 {
		return nil, det, fileErr(ErrEmptyFile, msgEmpty, nil)
	}
	if len(rows) > MaxRows {
		return nil, det, tooManyRows()
	}
	return rows, det, nil
}

// ParseText parses a list pasted into a text box: one student per line,
// numbered ("1. Иванов Иван", "1) Иванов Иван", "1;Иванов Иван") or bulleted
// ("- Иванов Иван") lists, or cells copied from Excel (tab, comma, semicolon
// or pipe separated, with or without a header). Row numbers are stripped and
// are never treated as an external ID; IDs are read only from a column whose
// header names one ("ID", "Табельный номер", "E-mail", ...).
// Callers should cap the input size (MaxFileSize) and reject a result with
// more than MaxRows rows — or use ParsePastedText, which does both.
func ParseText(text string) ([]Row, Detected) {
	text = strings.TrimPrefix(text, "\uFEFF")
	rows, det := parseTable(splitText(text))
	det.Format = FormatText
	det.Encoding = EncodingUTF8
	return rows, det
}

// ParsePastedText is ParseText with the limits of Parse: text longer than
// MaxFileSize bytes, without any student row or with more than MaxRows rows
// is rejected with a *FileError (ErrTooLarge, ErrEmptyFile, ErrTooManyRows).
func ParsePastedText(text string) ([]Row, Detected, error) {
	det := Detected{Format: FormatText, Encoding: EncodingUTF8}
	if len(text) > MaxFileSize {
		return nil, det, fileErr(ErrTooLarge, msgTextTooLarge, nil)
	}
	// Same guard as Parse: do not split a huge list into cells first.
	if nonBlankLines(text) > 2*(MaxRows+headerSearchRows) {
		return nil, det, tooManyRows()
	}
	rows, det := ParseText(text)
	if len(rows) == 0 {
		return nil, det, fileErr(ErrEmptyFile, msgEmptyText, nil)
	}
	if len(rows) > MaxRows {
		return nil, det, tooManyRows()
	}
	return rows, det, nil
}

// nonBlankLines counts lines (ended by LF, CRLF or a lone CR) that hold
// something besides whitespace: an upper bound of the records, since a record
// is never shorter than one line.
func nonBlankLines(text string) int {
	n, blank := 0, true
	for _, r := range text {
		switch {
		case r == '\n' || r == '\r':
			if !blank {
				n++
			}
			blank = true
		case !unicode.IsSpace(r):
			blank = false
		}
	}
	if !blank {
		n++
	}
	return n
}

func tooManyRows() error {
	return fileErr(ErrTooManyRows,
		fmt.Sprintf("В списке больше %d строк. Разделите его на несколько файлов.", MaxRows), nil)
}
