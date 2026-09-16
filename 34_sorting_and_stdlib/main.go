package main

// ============================================================================
// CONCEPT: the everyday standard library — `slices`, `maps`, `sort`,
// `strings`, `strconv`, `cmp`.
//
// WHY THIS MATTERS
// Go has no lodash, and until recently no built-in sort/search helpers
// either — you wrote for-loops. Go 1.21 added the generic `slices` and
// `maps` packages (built on the generics you learned in lesson 21), and
// they're now the idiomatic way to do 80% of collection work. Knowing them
// is the difference between writing 15 lines and writing 1.
//
// JS/TS comparison: `slices` is roughly Array.prototype + lodash, `maps` is
// Object.keys/values/entries, `strconv` is Number()/String()/parseInt with
// explicit errors, and `strings` is String.prototype.
// ============================================================================

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

type Employee struct {
	Name   string
	Dept   string
	Salary int
}

func main() {
	// ---------- slices: sorting ----------
	nums := []int{42, 7, 19, 7, 3, 88}
	fmt.Println("== slices: sorting ==")

	// slices.Sort sorts IN PLACE and works on any ordered type — no
	// comparator needed, and no sort.Ints/sort.Strings/sort.Float64s zoo.
	sorted := slices.Clone(nums) // Clone first; Sort mutates!
	slices.Sort(sorted)
	fmt.Println("original :", nums)
	fmt.Println("sorted   :", sorted)

	// slices.SortFunc for custom ordering. The comparator returns
	// negative / zero / positive — the same contract as JS's Array.sort.
	// cmp.Compare is the generic helper that produces exactly that.
	staff := []Employee{
		{"Ada", "eng", 180},
		{"Ben", "sales", 120},
		{"Cai", "eng", 210},
		{"Dee", "sales", 120},
	}
	slices.SortFunc(staff, func(a, b Employee) int {
		return cmp.Compare(b.Salary, a.Salary) // b,a = descending
	})
	fmt.Println("by salary desc:", staff)

	// Multi-key sort: fall through to the next key when the first ties.
	// cmp.Or returns the first non-zero value — perfect for this.
	slices.SortFunc(staff, func(a, b Employee) int {
		return cmp.Or(
			strings.Compare(a.Dept, b.Dept), // primary: dept A-Z
			cmp.Compare(b.Salary, a.Salary), // secondary: salary high->low
			strings.Compare(a.Name, b.Name), // tiebreak: name A-Z
		)
	})
	fmt.Println("dept, salary :", staff)

	// SortStableFunc preserves the relative order of equal elements — use
	// it when you're layering sorts or order-of-arrival matters.
	slices.SortStableFunc(staff, func(a, b Employee) int {
		return strings.Compare(a.Dept, b.Dept)
	})

	// ---------- slices: searching & manipulating ----------
	fmt.Println()
	fmt.Println("== slices: search & manipulate ==")
	fmt.Println("Contains(19)   :", slices.Contains(nums, 19))
	fmt.Println("Index(19)      :", slices.Index(nums, 19))
	fmt.Println("IndexFunc(>50) :", slices.IndexFunc(nums, func(n int) bool { return n > 50 }))
	fmt.Println("Max / Min      :", slices.Max(nums), slices.Min(nums)) // panics on an empty slice!
	fmt.Println("Equal          :", slices.Equal([]int{1, 2}, []int{1, 2}))
	fmt.Println("Reverse        :", reversed(nums))

	// BinarySearch needs a SORTED slice. Returns (index, found).
	i, found := slices.BinarySearch(sorted, 19)
	fmt.Printf("BinarySearch(19): index=%d found=%v\n", i, found)

	// Compact removes CONSECUTIVE duplicates — so sort first to dedupe.
	dedup := slices.Compact(slices.Clone(sorted))
	fmt.Println("deduped        :", dedup)

	// Insert / Delete / Concat avoid the old append-slicing tricks.
	fmt.Println("Insert at 1    :", slices.Insert(slices.Clone(dedup), 1, 5))
	fmt.Println("Delete [1:3)   :", slices.Delete(slices.Clone(dedup), 1, 3))
	fmt.Println("Concat         :", slices.Concat([]int{1, 2}, []int{3, 4}))

	// ---------- maps ----------
	// Remember from lesson 10: map iteration order is RANDOM. The standard
	// recipe for deterministic output is: collect keys, sort, iterate.
	fmt.Println()
	fmt.Println("== maps ==")
	stock := map[string]int{"bolts": 30, "nuts": 12, "washers": 75, "screws": 4}

	keys := slices.Sorted(maps.Keys(stock)) // maps.Keys returns an iterator (Go 1.23+)
	fmt.Println("sorted keys    :", keys)
	for _, k := range keys {
		fmt.Printf("  %-8s %d\n", k, stock[k])
	}

	vals := slices.Collect(maps.Values(stock))
	slices.Sort(vals)
	fmt.Println("sorted values  :", vals)

	// maps.Clone / maps.Equal / maps.Copy round out the set.
	cloned := maps.Clone(stock)
	delete(cloned, "nuts")
	fmt.Println("Equal after del:", maps.Equal(stock, cloned))

	// Sorting a map BY VALUE — very common ("top N"). Build a slice of
	// keys, then SortFunc using the map lookup.
	byQty := slices.Clone(keys)
	slices.SortFunc(byQty, func(a, b string) int { return cmp.Compare(stock[b], stock[a]) })
	fmt.Println("keys by qty    :", byQty)

	// ---------- strings ----------
	fmt.Println()
	fmt.Println("== strings ==")
	raw := "  Hello, Go World!  "
	fmt.Printf("TrimSpace   %q\n", strings.TrimSpace(raw))
	fmt.Printf("ToUpper     %q\n", strings.ToUpper(raw))
	fmt.Printf("Split       %q\n", strings.Split("a,b,,c", ","))
	fmt.Printf("Fields      %q\n", strings.Fields(raw)) // split on any run of whitespace
	fmt.Printf("Join        %q\n", strings.Join([]string{"a", "b", "c"}, " | "))
	fmt.Printf("ReplaceAll  %q\n", strings.ReplaceAll(raw, "o", "0"))
	fmt.Printf("Contains %v  HasPrefix %v  Cut ",
		strings.Contains(raw, "Go"), strings.HasPrefix(raw, "  H"))

	// strings.Cut is the clean way to split "key=value" exactly once.
	before, after, ok := strings.Cut("DATABASE_URL=postgres://localhost", "=")
	fmt.Printf("%q / %q (ok=%v)\n", before, after, ok)

	// Building a string in a loop: use strings.Builder, NOT `s += x`.
	// Strings are immutable, so += allocates a new string every iteration —
	// O(n^2). Builder is O(n).
	var b strings.Builder
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "item-%d;", i) // Builder implements io.Writer
	}
	fmt.Println("Builder    :", b.String())

	// ---------- strconv ----------
	// Go never silently coerces. Every string<->number conversion is
	// explicit and returns an error.
	fmt.Println()
	fmt.Println("== strconv ==")
	if n, err := strconv.Atoi("123"); err == nil {
		fmt.Println("Atoi         :", n+1)
	}
	if _, err := strconv.Atoi("12a"); err != nil {
		fmt.Println("Atoi error   :", err) // strconv.Atoi: parsing "12a": invalid syntax
	}
	f, _ := strconv.ParseFloat("3.14159", 64)
	fmt.Println("ParseFloat   :", f)
	bl, _ := strconv.ParseBool("true") // accepts 1,t,T,TRUE,true,True + the false set
	fmt.Println("ParseBool    :", bl)
	fmt.Println("Itoa         :", strconv.Itoa(42)+"!")
	fmt.Println("FormatFloat  :", strconv.FormatFloat(f, 'f', 2, 64)) // "3.14"
	fmt.Println("Quote        :", strconv.Quote(`he said "hi"`))

	// NOTE: string(65) is NOT "65" — it's "A" (a rune conversion). Use
	// strconv.Itoa for numbers. `go vet` flags the classic mistake.
	fmt.Println("string(65)   :", string(rune(65)), "<- a rune, not a number!")
}

func reversed(in []int) []int {
	out := slices.Clone(in)
	slices.Reverse(out)
	return out
}

// ----------------------------------------------------------------------------
// CHEAT SHEET: JS -> Go
//   arr.sort((a,b)=>a-b)        slices.Sort / slices.SortFunc
//   arr.includes(x)             slices.Contains
//   arr.indexOf(x)              slices.Index
//   arr.findIndex(fn)           slices.IndexFunc
//   arr.reverse()               slices.Reverse (in place)
//   [...new Set(arr)]           slices.Sort then slices.Compact
//   Math.max(...arr)            slices.Max
//   Object.keys(o)              slices.Collect(maps.Keys(m))
//   {...o}                      maps.Clone
//   arr.join(",")               strings.Join
//   s.trim()                    strings.TrimSpace
//   parseInt(s)                 strconv.Atoi (returns an error!)
//
// There is STILL no map/filter/reduce over slices that returns a slice.
// Write the loop, or use the iterator helpers in `slices`/`iter` (Go 1.23+).
// Idiomatic Go prefers the explicit loop; it's clearer and allocates less.
//
// GOTCHAS
//   1. slices.Sort MUTATES. Clone first if you need the original.
//   2. slices.Max/Min PANIC on an empty slice — check len() first.
//   3. slices.Compact only removes ADJACENT duplicates — sort first.
//   4. Never build strings with += in a loop; use strings.Builder.
// ----------------------------------------------------------------------------
