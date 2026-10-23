package app

import (
	"fmt"
	"math"
	"sort"
)

const schLongWireThreshold = 100.0

// longWireFindings reports one advisory per wire primitive whose longest
// orthogonal segment exceeds 100 raw units. The threshold applies to a real
// segment, not the full bounding span of an L-shaped or branched polyline, so a
// compact bend is not mistaken for a long run. This is deliberately INFO:
// length alone never authorizes moving a functional group or replacing an
// electrical connection with labels.
func longWireFindings(wires []schGroupWire) []checkFinding {
	var out []checkFinding
	for _, wire := range wires {
		var longest [4]float64
		longestLen := 0.0
		for _, seg := range labelWireSegments(wire) {
			length := math.Hypot(seg[2]-seg[0], seg[3]-seg[1])
			if length > longestLen {
				longest, longestLen = seg, length
			}
		}
		if longestLen <= schLongWireThreshold {
			continue
		}
		net := wire.Net
		if net == "" {
			net = "?"
		}
		out = append(out, checkFinding{
			Type:            "long-wire",
			Level:           "info",
			WirePrimitiveId: wire.ID,
			WireNet:         wire.Net,
			WireSegment:     []float64{longest[0], longest[1], longest[2], longest[3]},
			WireLength:      longestLen,
			At: &checkPoint{
				X: (longest[0] + longest[2]) / 2,
				Y: (longest[1] + longest[3]) / 2,
			},
			Message: fmt.Sprintf(
				"网络 %q 的导线最长单段 %.1f raw > %.0f raw；回源检查能否移动完整功能组、改为更短直连或使用同名标签；长度本身不是移动理由",
				net, longestLen, schLongWireThreshold),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].WireLength != out[j].WireLength {
			return out[i].WireLength > out[j].WireLength
		}
		return out[i].WirePrimitiveId < out[j].WirePrimitiveId
	})
	return out
}
