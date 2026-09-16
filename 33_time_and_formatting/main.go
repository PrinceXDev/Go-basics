package main

// ============================================================================
// CONCEPT: the `time` package — Time, Duration, the reference layout,
// monotonic clocks, timers and tickers.
//
// WHY THIS MATTERS
// Every backend touches time: timestamps in the DB, timeouts, retries with
// backoff, rate limits, scheduled jobs, "created 5 minutes ago". Go's time
// package is excellent but has ONE famously weird design choice (the
// reference layout) that trips up every newcomer exactly once.
//
// JS/TS comparison: JS `Date` is a millisecond number with a terrible API,
// so everyone installs date-fns/dayjs/luxon. Go's stdlib `time` is good
// enough that third-party date libraries are rare. Also: JS has no real
// Duration type — you pass bare milliseconds. Go has `time.Duration`, a
// distinct type, so `5 * time.Second` can't be confused with `5`.
// ============================================================================

import (
	"fmt"
	"time"
)

func main() {
	// ---------- time.Time ----------
	// time.Now() returns a time.Time: an instant, with nanosecond
	// precision, a location (timezone), AND a hidden monotonic reading.
	now := time.Now()
	fmt.Println("== time.Time ==")
	fmt.Println("now            :", now)
	fmt.Println("UTC            :", now.UTC())
	fmt.Println("Unix seconds   :", now.Unix())
	fmt.Println("UnixMilli (JS!):", now.UnixMilli()) // what Date.now() gives you
	fmt.Println("year/month/day :", now.Year(), now.Month(), now.Day())
	fmt.Println("weekday        :", now.Weekday())

	// Constructing a specific time. Note: time.Date takes a *Location.
	launch := time.Date(2026, time.March, 14, 9, 30, 0, 0, time.UTC)
	fmt.Println("explicit time  :", launch)

	// ---------- THE REFERENCE LAYOUT (the weird bit) ----------
	// Go does NOT use strftime (%Y-%m-%d) or moment tokens (YYYY-MM-DD).
	// Instead you write out an EXAMPLE of the format, using one specific
	// reference instant:
	//
	//     Mon Jan 2 15:04:05 MST 2006
	//     ---  --- - -- -- -- --- ----
	//      |    |  |  |  |  |  |    +-- 2006 = year
	//      |    |  |  |  |  |  +------- MST  = timezone
	//      |    |  |  |  |  +---------- 05   = second
	//      |    |  |  |  +------------- 04   = minute
	//      |    |  |  +---------------- 15   = hour (24h; use 03 for 12h)
	//      |    |  +------------------- 2    = day   (02 = zero-padded)
	//      |    +---------------------- Jan  = month (01 = numeric)
	//      +---------------------------- Mon  = weekday
	//
	// Mnemonic: 1 2 3 4 5 6 7 -> month day hour minute second year zone.
	fmt.Println()
	fmt.Println("== formatting (reference layout) ==")
	fmt.Println("RFC3339        :", launch.Format(time.RFC3339)) // use this in APIs
	fmt.Println("date only      :", launch.Format("2006-01-02"))
	fmt.Println("human          :", launch.Format("Mon, 02 Jan 2006 15:04"))
	fmt.Println("12-hour        :", launch.Format("03:04 PM"))
	fmt.Println("with millis    :", launch.Format("2006-01-02 15:04:05.000"))

	// Parsing uses the SAME layout string. Parse returns an error — always
	// check it; a layout/value mismatch is a very common runtime bug.
	parsed, err := time.Parse("2006-01-02", "2026-12-25")
	if err != nil {
		fmt.Println("parse failed:", err)
	} else {
		fmt.Println("parsed         :", parsed.Format(time.RFC1123))
	}

	// time.Parse with no zone in the layout assumes UTC. time.ParseInLocation
	// lets you say "this wall-clock string is in THIS zone".
	nz, _ := time.LoadLocation("Pacific/Auckland")
	local, _ := time.ParseInLocation("2006-01-02 15:04", "2026-12-25 09:00", nz)
	fmt.Println("in Auckland    :", local, "->UTC:", local.UTC().Format(time.RFC3339))

	// ---------- time.Duration ----------
	// A Duration is an int64 count of NANOSECONDS, but it's its own type so
	// the compiler stops you mixing it up with a plain number.
	fmt.Println()
	fmt.Println("== Duration ==")
	d := 90 * time.Second
	fmt.Println("d              :", d)                // "1m30s" — Duration has a nice String()
	fmt.Println("d.Minutes()    :", d.Minutes())      // 1.5 (float64)
	fmt.Println("d.Milliseconds :", d.Milliseconds()) // 90000

	// You CANNOT write `n * time.Second` where n is an int variable —
	// mismatched types. Convert explicitly:
	n := 3
	fmt.Println("n seconds      :", time.Duration(n)*time.Second)

	// ParseDuration reads config strings like "30s", "5m", "1h30m".
	timeout, _ := time.ParseDuration("1h30m")
	fmt.Println("parsed duration:", timeout)

	// ---------- arithmetic ----------
	fmt.Println()
	fmt.Println("== arithmetic ==")
	deadline := launch.Add(48 * time.Hour) // Time + Duration = Time
	fmt.Println("deadline       :", deadline.Format(time.RFC3339))
	fmt.Println("Sub -> Duration:", deadline.Sub(launch)) // Time - Time = Duration
	fmt.Println("Before/After   :", launch.Before(deadline), deadline.After(launch))

	// AddDate handles calendar-aware maths (months/years vary in length).
	fmt.Println("+1 month       :", launch.AddDate(0, 1, 0).Format("2006-01-02"))

	// Truncate/Round snap a time to a Duration boundary — handy for bucketing
	// metrics by minute/hour. NOTE: Truncate works on absolute time since the
	// epoch, NOT on the local wall clock, so in a half-hour-offset zone like
	// IST (+05:30) "truncate to hour" lands on :30, not :00. To bucket by
	// local wall clock, convert to UTC or do the maths on the clock fields.
	fmt.Println("truncate to hr :", now.Truncate(time.Hour).Format("15:04:05"))

	// Comparing times: use Equal(), NOT ==. Two Times can represent the same
	// instant but differ in location/monotonic reading, making == false.
	// `now` carries a monotonic reading; now.UTC() strips it, so == is false
	// even though both describe the exact same instant.
	stripped := now.UTC()
	fmt.Println("now == now.UTC():", now == stripped)
	fmt.Println("now.Equal(...)  :", now.Equal(stripped))

	// ---------- monotonic clock ----------
	// For measuring ELAPSED time, always use time.Since / t.Sub. They use a
	// hidden monotonic reading that is immune to NTP adjustments and DST —
	// unlike subtracting two wall-clock timestamps.
	fmt.Println()
	fmt.Println("== measuring elapsed time ==")
	start := time.Now()
	time.Sleep(25 * time.Millisecond)
	fmt.Printf("elapsed        : %v (%.1fms)\n", time.Since(start), float64(time.Since(start).Microseconds())/1000)

	// ---------- timers and tickers ----------
	// time.After returns a channel that fires once — you already used this
	// in lesson 24's select statements.
	fmt.Println()
	fmt.Println("== Ticker (repeating) ==")
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop() // ALWAYS stop a ticker or it leaks its goroutine
	done := time.After(70 * time.Millisecond)

	for tick := 1; ; tick++ {
		select {
		case t := <-ticker.C:
			fmt.Printf("  tick %d at %s\n", tick, t.Format("05.000"))
		case <-done:
			fmt.Println("  done")
			return
		}
	}
}

// ----------------------------------------------------------------------------
// GOTCHAS WORTH MEMORISING
//   1. Layout is an EXAMPLE, not tokens: "2006-01-02", never "YYYY-MM-DD".
//   2. Use t1.Equal(t2), never t1 == t2.
//   3. time.Duration(n) * time.Second — you must convert an int variable.
//   4. Store timestamps in UTC; convert to a local zone only for display.
//   5. Always `defer ticker.Stop()`. time.Tick() (no Stop) leaks — avoid it
//      outside of throwaway programs.
//   6. time.Sleep in a test makes it slow AND flaky. Inject a clock or use
//      shorter durations + channels instead.
//   7. JSON: time.Time marshals as RFC3339 automatically. A DB `DATE`
//      column usually needs a custom type or a string.
// ----------------------------------------------------------------------------
