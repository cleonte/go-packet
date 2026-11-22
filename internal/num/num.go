// Package num has the small helpers for optional numbers (nil means "no reading") used
// throughout Packet.
package num

import "math"

// Ptr returns a pointer to v.
func Ptr(v float64) *float64 { return &v }

// Val dereferences an optional number, with a default for nil.
func Val(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

// Round1 rounds to one decimal for JSON, or nil for nil.
func Round1(p *float64) any {
	if p == nil {
		return nil
	}
	return math.Round(*p*10) / 10
}
