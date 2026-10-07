import zlib from 'node:zlib'

// Files the tests feed in (Windows-1251 CSV) and read back (export CSV/XLSX).

/** Encodes text as Windows-1251 (what Russian Excel saves "CSV (разделители — точка с запятой)" in). */
export function encodeCp1251(text: string): Buffer {
  const out: number[] = []
  for (const ch of text) {
    const c = ch.codePointAt(0)!
    if (c < 0x80) out.push(c)
    else if (c >= 0x0410 && c <= 0x044f) out.push(c - 0x0410 + 0xc0) // А..я
    else if (c === 0x0401) out.push(0xa8) // Ё
    else if (c === 0x0451) out.push(0xb8) // ё
    else if (c === 0x2116) out.push(0xb9) // №
    else if (c === 0x00a0) out.push(0xa0)
    else throw new Error(`no cp1251 code for ${JSON.stringify(ch)}`)
  }
  return Buffer.from(out)
}

/** Parses the lesson export CSV: UTF-8 BOM, ';' separated, CRLF, RFC 4180 quoting. */
/**
 * A cell of an exported row by column title. Time columns may name their zone
 * ("Первый вход (UTC+3)"): the plain title matches those too.
 */
export function exportCell(row: Record<string, string> | undefined, title: string): string | undefined {
  if (!row) return undefined
  if (title in row) return row[title]
  const key = Object.keys(row).find((k) => k.startsWith(`${title} (`))
  return key === undefined ? undefined : row[key]
}

export function parseExportCsv(body: Buffer): { bom: boolean; header: string[]; rows: Record<string, string>[] } {
  const bom = body[0] === 0xef && body[1] === 0xbb && body[2] === 0xbf
  const text = body.toString('utf8').replace(/^﻿/, '')
  const records: string[][] = []
  let field = ''
  let record: string[] = []
  let quoted = false
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    if (quoted) {
      if (ch === '"') {
        if (text[i + 1] === '"') {
          field += '"'
          i++
        } else quoted = false
      } else field += ch
    } else if (ch === '"') quoted = true
    else if (ch === ';') {
      record.push(field)
      field = ''
    } else if (ch === '\n' || ch === '\r') {
      if (ch === '\r' && text[i + 1] === '\n') i++
      record.push(field)
      records.push(record)
      record = []
      field = ''
    } else field += ch
  }
  if (field !== '' || record.length > 0) {
    record.push(field)
    records.push(record)
  }
  const [header = [], ...rest] = records
  const rows = rest.map((r) => Object.fromEntries(header.map((h, i) => [h, r[i] ?? ''])))
  return { bom, header, rows }
}

/**
 * Reads one file out of a ZIP archive (an .xlsx is a ZIP): walks the central
 * directory and inflates the entry. Enough for checking export contents.
 */
export function readZipEntry(zip: Buffer, name: string): Buffer | null {
  // End of central directory record: signature 0x06054b50, searched from the end.
  let eocd = -1
  for (let i = zip.length - 22; i >= Math.max(0, zip.length - 65_557); i--) {
    if (zip.readUInt32LE(i) === 0x06054b50) {
      eocd = i
      break
    }
  }
  if (eocd < 0) throw new Error('not a ZIP file (no end of central directory)')
  const count = zip.readUInt16LE(eocd + 10)
  let p = zip.readUInt32LE(eocd + 16)
  for (let n = 0; n < count; n++) {
    if (zip.readUInt32LE(p) !== 0x02014b50) throw new Error('broken ZIP central directory')
    const method = zip.readUInt16LE(p + 10)
    const compressedSize = zip.readUInt32LE(p + 20)
    const nameLen = zip.readUInt16LE(p + 28)
    const extraLen = zip.readUInt16LE(p + 30)
    const commentLen = zip.readUInt16LE(p + 32)
    const localOffset = zip.readUInt32LE(p + 42)
    const entryName = zip.subarray(p + 46, p + 46 + nameLen).toString('utf8')
    if (entryName === name) {
      const lNameLen = zip.readUInt16LE(localOffset + 26)
      const lExtraLen = zip.readUInt16LE(localOffset + 28)
      const start = localOffset + 30 + lNameLen + lExtraLen
      const data = zip.subarray(start, start + compressedSize)
      if (method === 0) return Buffer.from(data)
      if (method === 8) return zlib.inflateRawSync(data)
      throw new Error(`unsupported ZIP method ${method}`)
    }
    p += 46 + nameLen + extraLen + commentLen
  }
  return null
}

function xmlText(s: string): string {
  return s
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&#(\d+);/g, (_, n: string) => String.fromCodePoint(Number(n)))
    .replace(/&#x([0-9a-f]+);/gi, (_, n: string) => String.fromCodePoint(parseInt(n, 16)))
    .replace(/&amp;/g, '&')
}

/** The text of every <t> inside `xml`, concatenated (plain and rich-text strings). */
function tTexts(xml: string): string {
  return [...xml.matchAll(/<t\b[^>]*>([\s\S]*?)<\/t>/g)].map((m) => xmlText(m[1])).join('')
}

function columnIndex(ref: string): number {
  const letters = /^[A-Z]+/.exec(ref)?.[0] ?? 'A'
  let n = 0
  for (const ch of letters) n = n * 26 + (ch.charCodeAt(0) - 64)
  return n - 1
}

/**
 * The cell values of one worksheet of an .xlsx, by sheet name, as rows of
 * strings (row 1 first; missing cells are ""). Shared, inline and formula
 * strings and numbers are read as their text; styles are ignored.
 */
export function readXlsxSheet(xlsx: Buffer, sheetName: string): string[][] {
  const entry = (name: string) => {
    const data = readZipEntry(xlsx, name)
    if (!data) throw new Error(`xlsx: no ${name}`)
    return data.toString('utf8')
  }
  const workbook = entry('xl/workbook.xml')
  const sheet = [...workbook.matchAll(/<sheet\b([^>]*)\/?>/g)]
    .map((m) => m[1])
    .find((attrs) => xmlText(/\bname="([^"]*)"/.exec(attrs)?.[1] ?? '') === sheetName)
  const relId = sheet && /\br:id="([^"]*)"/.exec(sheet)?.[1]
  if (!relId) throw new Error(`xlsx: no sheet «${sheetName}»`)
  const rels = entry('xl/_rels/workbook.xml.rels')
  const rel = [...rels.matchAll(/<Relationship\b([^>]*)\/?>/g)].map((m) => m[1]).find((a) => a.includes(`Id="${relId}"`))
  const target = rel && /\bTarget="([^"]*)"/.exec(rel)?.[1]
  if (!target) throw new Error(`xlsx: no relationship ${relId}`)
  const path = target.startsWith('/') ? target.slice(1) : `xl/${target}`

  const sharedXml = readZipEntry(xlsx, 'xl/sharedStrings.xml')?.toString('utf8') ?? ''
  const shared = [...sharedXml.matchAll(/<si\b[^>]*>([\s\S]*?)<\/si>/g)].map((m) => tTexts(m[1]))

  const rows: string[][] = []
  for (const row of entry(path).matchAll(/<row\b([^>]*?)(?:\/>|>([\s\S]*?)<\/row>)/g)) {
    const r = Number(/\br="(\d+)"/.exec(row[1])?.[1])
    const cells: string[] = []
    for (const c of (row[2] ?? '').matchAll(/<c\b([^>]*?)(?:\/>|>([\s\S]*?)<\/c>)/g)) {
      const attrs = c[1]
      const inner = c[2] ?? ''
      const ref = /\br="([A-Z]+\d+)"/.exec(attrs)?.[1] ?? ''
      const type = /\bt="([^"]*)"/.exec(attrs)?.[1] ?? 'n'
      const v = /<v>([\s\S]*?)<\/v>/.exec(inner)?.[1]
      let text = ''
      if (type === 's') text = v === undefined ? '' : (shared[Number(v)] ?? '')
      else if (type === 'inlineStr') text = tTexts(inner)
      else text = v === undefined ? '' : xmlText(v)
      const col = columnIndex(ref)
      while (cells.length < col) cells.push('')
      cells[col] = text
    }
    while (rows.length < r - 1) rows.push([])
    rows[r - 1] = cells
  }
  return rows
}
