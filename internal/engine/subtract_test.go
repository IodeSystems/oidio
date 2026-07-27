package engine

import "testing"

// Subtract's callers build `others` by walking a segment list, so the intervals
// arrive in whatever order the diarizer emitted them, overlapping each other and
// running past both ends of the target. All three must be handled here rather
// than by every caller remembering to pre-clean its input.
func TestSubtractHandlesUnsortedOverlappingUnclippedInput(t *testing.T) {
	got := Subtract(10, 20, [][2]float64{
		{18, 30}, // runs past the end
		{12, 14}, // out of order
		{13, 15}, // overlaps the previous
		{0, 11},  // runs past the start
	}, 0.25)
	want := [][2]float64{{11, 12}, {15, 18}}
	if len(got) != len(want) {
		t.Fatalf("Subtract = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Subtract = %v, want %v", got, want)
		}
	}
}

func TestSubtractFullyCoveredYieldsNothing(t *testing.T) {
	if got := Subtract(5, 9, [][2]float64{{4, 10}}, 0.25); len(got) != 0 {
		t.Fatalf("fully covered interval should yield nothing, got %v", got)
	}
}

func TestSubtractNoOverlapKeepsWholeInterval(t *testing.T) {
	got := Subtract(5, 9, [][2]float64{{0, 4}, {10, 12}}, 0.25)
	if len(got) != 1 || got[0] != [2]float64{5, 9} {
		t.Fatalf("disjoint others should keep the interval, got %v", got)
	}
}

// minPart is a floor on the REMAINDER, not on the subtracted piece: the point is
// to avoid emitting slivers, whatever produced them.
func TestSubtractDropsSliversBelowMinPart(t *testing.T) {
	got := Subtract(0, 10, [][2]float64{{0.1, 5}}, 0.25)
	if len(got) != 1 || got[0] != [2]float64{5, 10} {
		t.Fatalf("0.1s head sliver should be dropped, got %v", got)
	}
}
