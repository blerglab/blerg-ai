// JSON reporter retains durations; this small companion retains retry evidence.
import { writeFileSync } from 'node:fs'

export default class GateRetryReporter {
  retries = []

  onTestCaseResult(testCase) {
    const diagnostic = testCase.diagnostic()
    if (diagnostic?.retryCount > 0) {
      this.retries.push({
        package: testCase.module.moduleId,
        test: testCase.fullName,
        retries: diagnostic.retryCount,
        rerun_passed: testCase.result().state === 'passed',
      })
    }
  }

  onTestRunEnd() {
    const path = process.env.BLERG_GATE_VITEST_RERUNS
    try {
      if (path) writeFileSync(path, JSON.stringify(this.retries) + '\n')
    } catch {
      console.warn('Gate retry telemetry unavailable')
    }
  }
}
