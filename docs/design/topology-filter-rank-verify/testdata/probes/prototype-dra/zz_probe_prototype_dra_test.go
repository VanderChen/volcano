/*
Prototype probe for issue 004: scenario of P6 (probes/baseline-dra) on the
ranked path.
*/

package allocate

import "testing"

// Q8: scenario of P6 (needs probes/baseline-dra). The ranked path keeps the
// winner's live statement, so the Reserve-style plugin sees its own state.
func TestProtoQ8ReplayWithDRA(t *testing.T) {
	for _, tc := range draReplayCases {
		for _, mode := range []protoMode{modeExhaustive, modeRanked} {
			res := draReplayScenario(t, mode.action(), tc.busyRack)
			t.Logf("%-45s mode=%-10s placed=%v bindCheck=%v counters: %s", tc.name, mode.name, res.placed, res.bindErr, res.counters)
			if mode.rank && res.bindErr != nil {
				t.Errorf("%s: ranked mode failed to bind: %v", tc.name, res.bindErr)
			}
		}
	}
}
