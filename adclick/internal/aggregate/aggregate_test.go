package aggregate

import (
	"testing"
	"time"

	"adclick/internal/event"
)

func click(ad, country string, at time.Time) event.Click {
	return event.Click{AdID: ad, CampaignID: "c", AdvertiserID: "a", Country: country, DeviceType: "mobile", ClickedAt: at}
}

func TestBuildGroupsAcrossGranularities(t *testing.T) {
	base := time.Date(2026, 1, 1, 10, 30, 15, 0, time.UTC)
	evs := []event.Click{
		click("ad1", "US", base),
		click("ad1", "US", base.Add(10*time.Second)), // same minute
		click("ad1", "US", base.Add(2*time.Minute)),  // same hour, new minute
		click("ad1", "DE", base),                     // other country
	}
	d := Build(evs, []bool{true, false, true, true})

	total := map[string]int64{}
	uniq := map[string]int64{}
	rows := map[string]int{}
	for _, x := range d {
		total[x.Granularity] += x.Clicks
		uniq[x.Granularity] += x.Unique
		rows[x.Granularity]++
	}
	for _, g := range []string{"minute", "hour", "day"} {
		if total[g] != 4 || uniq[g] != 3 {
			t.Errorf("%s: clicks=%d unique=%d", g, total[g], uniq[g])
		}
	}
	if rows["minute"] != 3 || rows["hour"] != 2 || rows["day"] != 2 {
		t.Errorf("rows: %v", rows)
	}
}

func TestBuildUsesEventTimeForLateEvents(t *testing.T) {
	late := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	d := Build([]event.Click{click("ad1", "US", late)}, nil)
	for _, x := range d {
		if x.Granularity == "day" && !x.Bucket.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("day bucket %v", x.Bucket)
		}
	}
}

func TestBuildIsSorted(t *testing.T) {
	now := time.Now()
	d := Build([]event.Click{click("b", "US", now), click("a", "US", now)}, nil)
	for i := 1; i < len(d); i++ {
		if less(d[i].Key, d[i-1].Key) {
			t.Fatal("not sorted")
		}
	}
}
