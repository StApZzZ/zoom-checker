import { FileSpreadsheet, FileText, Loader2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { useLessonExport } from '@/lib/queries'

/** Export buttons: Excel first (what most teachers open), CSV second. */
export function ExportButtons({ lessonId, size = 'lg' }: { lessonId: number; size?: 'default' | 'lg' }) {
  const exportFile = useLessonExport(lessonId)
  const xlsx = exportFile.pending.includes('xlsx')
  const csv = exportFile.pending.includes('csv')
  return (
    <>
      <Button size={size} onClick={() => exportFile.download('xlsx')} disabled={xlsx} aria-busy={xlsx || undefined}>
        {xlsx ? <Loader2 className="animate-spin" aria-hidden="true" /> : <FileSpreadsheet aria-hidden="true" />}
        Скачать Excel
      </Button>
      <Button
        size={size}
        variant="outline"
        onClick={() => exportFile.download('csv')}
        disabled={csv}
        aria-busy={csv || undefined}
      >
        {csv ? <Loader2 className="animate-spin" aria-hidden="true" /> : <FileText aria-hidden="true" />}
        Скачать CSV
      </Button>
    </>
  )
}
