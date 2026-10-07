// Measured numbers of the run (S1 latency, …): appended by tests, summarized by
// the global teardown into test-results/e2e-metrics.json.

import fs from 'node:fs'
import path from 'node:path'

import type { TestInfo } from '@playwright/test'

import { runDir } from './env'

export interface Metric {
  name: string
  value: number
  unit: string
  test?: string
}

export function metricsFile(dir = runDir()): string {
  return path.join(dir, 'metrics.jsonl')
}

/** Records a metric (annotation on the test + line in the run's metrics file). */
export function recordMetric(testInfo: TestInfo, name: string, value: number, unit = 'ms'): void {
  const m: Metric = { name, value, unit, test: testInfo.titlePath.slice(1).join(' › ') }
  testInfo.annotations.push({ type: 'metric', description: `${name} = ${value} ${unit}` })
  fs.appendFileSync(metricsFile(), JSON.stringify(m) + '\n')
  console.log(`[metric] ${name} = ${value} ${unit}`)
}

export function readMetrics(dir: string): Metric[] {
  const file = metricsFile(dir)
  if (!fs.existsSync(file)) return []
  return fs
    .readFileSync(file, 'utf8')
    .split('\n')
    .filter(Boolean)
    .map((l) => JSON.parse(l) as Metric)
}
