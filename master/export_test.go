// SPDX-License-Identifier: MIT

package master

// NetworkBootRuns returns the number of network boot-up goroutines
// running in m.
func NetworkBootRuns(m *Master) int {
	if m.mgr == nil {
		return 0
	}
	return int(m.mgr.runs.Load())
}
