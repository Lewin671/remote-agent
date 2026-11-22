// Package sortutil provides tiny, reflection-free and non-generic sorting
// helpers for WASM-targeted packages where stdlib sort pulls internal/reflectlite
// and generic slice sorters monomorphize per element type.
package sortutil

// Interface allows sorting any indexable collection without allocating closures.
type Interface interface {
	Len() int
	Less(i, j int) bool
	Swap(i, j int)
}

func siftInterface(data Interface, lo, hi int) {
	root := lo
	for {
		child := 2*root + 1
		if child >= hi {
			return
		}
		if child+1 < hi && data.Less(child, child+1) {
			child++
		}
		if !data.Less(root, child) {
			return
		}
		data.Swap(root, child)
		root = child
	}
}

// SortInterface sorts data in place using non-recursive heapsort without closures.
// Equal-key relative order is not guaranteed; include a tie-breaker or use
// StableInterface when equal elements must keep their input order.
//
//go:noinline
func SortInterface(data Interface) {
	n := data.Len()
	if n < 2 {
		return
	}
	for i := (n - 2) / 2; i >= 0; i-- {
		siftInterface(data, i, n)
	}
	for hi := n - 1; hi > 0; hi-- {
		data.Swap(0, hi)
		siftInterface(data, 0, hi)
	}
}

// Sort sorts n elements in place using non-recursive heapsort (O(n log n) worst case).
// Equal-key relative order is not guaranteed; intended for small domain slices
// where binary size matters more than stable ties.
//
//go:noinline
func Sort(n int, less func(i, j int) bool, swap func(i, j int)) {
	if n < 2 {
		return
	}
	sift := func(lo, hi int) {
		root := lo
		for {
			child := 2*root + 1
			if child >= hi {
				return
			}
			if child+1 < hi && less(child, child+1) {
				child++
			}
			if !less(root, child) {
				return
			}
			swap(root, child)
			root = child
		}
	}
	for i := (n - 2) / 2; i >= 0; i-- {
		sift(i, n)
	}
	for hi := n - 1; hi > 0; hi-- {
		swap(0, hi)
		sift(0, hi)
	}
}

// StableInterface stably sorts small slices in place using insertion sort (O(n^2) swaps) without closures.
//
//go:noinline
func StableInterface(data Interface) {
	n := data.Len()
	for i := 1; i < n; i++ {
		for j := i; j > 0 && data.Less(j, j-1); j-- {
			data.Swap(j, j-1)
		}
	}
}

//go:noinline
func Strings(a []string) {
	Sort(len(a), func(i, j int) bool { return a[i] < a[j] }, func(i, j int) { a[i], a[j] = a[j], a[i] })
}

//go:noinline
func Ints(a []int) {
	Sort(len(a), func(i, j int) bool { return a[i] < a[j] }, func(i, j int) { a[i], a[j] = a[j], a[i] })
}

//go:noinline
func Int32s(a []int32) {
	Sort(len(a), func(i, j int) bool { return a[i] < a[j] }, func(i, j int) { a[i], a[j] = a[j], a[i] })
}

//go:noinline
func Int64s(a []int64) {
	Sort(len(a), func(i, j int) bool { return a[i] < a[j] }, func(i, j int) { a[i], a[j] = a[j], a[i] })
}

//go:noinline
func Uint64s(a []uint64) {
	Sort(len(a), func(i, j int) bool { return a[i] < a[j] }, func(i, j int) { a[i], a[j] = a[j], a[i] })
}

//go:noinline
func Float64s(a []float64) {
	Sort(len(a), func(i, j int) bool { return a[i] < a[j] || (a[i] != a[i] && a[j] == a[j]) }, func(i, j int) { a[i], a[j] = a[j], a[i] })
}
