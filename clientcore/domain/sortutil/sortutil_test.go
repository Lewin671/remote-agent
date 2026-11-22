package sortutil

import (
	"math"
	"slices"
	"testing"
)

func TestSortAndTypedHelpers(t *testing.T) {
	s := []string{"c", "a", "b", "aa", ""}
	Strings(s)
	if !slices.Equal(s, []string{"", "a", "aa", "b", "c"}) {
		t.Fatalf("Strings mismatch: %v", s)
	}
	u := []uint64{9, 1, 5, 1, 3, 2, 8, 7, 6, 4}
	Uint64s(u)
	if !slices.Equal(u, []uint64{1, 1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("Uint64s mismatch: %v", u)
	}
	f := []float64{3.5, math.NaN(), 1.2, -0.5, 1.2}
	Float64s(f)
	if !math.IsNaN(f[0]) || f[1] != -0.5 || f[2] != 1.2 || f[3] != 1.2 || f[4] != 3.5 {
		t.Fatalf("Float64s mismatch: %v", f)
	}
	Ints([]int(nil))
	Int32s([]int32{2, 1})
	Int64s([]int64{2, 1})
}

type testStableItems []struct {
	key int
	seq int
}

func (s testStableItems) Len() int           { return len(s) }
func (s testStableItems) Less(i, j int) bool { return s[i].key < s[j].key }
func (s testStableItems) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func TestStablePreservesEqualOrder(t *testing.T) {
	items := testStableItems{{2, 0}, {1, 1}, {2, 2}, {1, 3}, {2, 4}, {0, 5}}
	StableInterface(items)
	want := testStableItems{{0, 5}, {1, 1}, {1, 3}, {2, 0}, {2, 2}, {2, 4}}
	if !slices.Equal(items, want) {
		t.Fatalf("StableInterface mismatch: got=%v want=%v", items, want)
	}
}
