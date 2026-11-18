package event

import (
	"errors"
	"testing"
	"time"
)

func valid(now time.Time) Click {
	return Click{
		EventID: NewID(), AdID: "ad-1", ClickedAt: now, Country: "US",
		DeviceType: "mobile", Browser: "chrome", Source: "web", IPHash: "abc",
	}
}

func TestValidate(t *testing.T) {
	now := time.Now()
	l := Limits{MaxAge: time.Hour, FutureSkew: time.Minute}

	cases := map[string]func(*Click){
		"ok":              func(c *Click) {},
		"bad uuid":        func(c *Click) { c.EventID = "nope" },
		"no ad":           func(c *Click) { c.AdID = "" },
		"lowercase ctry":  func(c *Click) { c.Country = "us" },
		"bad device":      func(c *Click) { c.DeviceType = "fridge" },
		"too old":         func(c *Click) { c.ClickedAt = now.Add(-2 * time.Hour) },
		"future":          func(c *Click) { c.ClickedAt = now.Add(time.Hour) },
		"no ip hash":      func(c *Click) { c.IPHash = "" },
		"zero clicked_at": func(c *Click) { c.ClickedAt = time.Time{} },
	}
	for name, mutate := range cases {
		c := valid(now)
		mutate(&c)
		err := c.Validate(now, l)
		if name == "ok" {
			if err != nil {
				t.Errorf("ok case failed: %v", err)
			}
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestVisitorPrefersUser(t *testing.T) {
	c := Click{UserID: "u1", IPHash: "h"}
	if c.Visitor() != "u:u1" {
		t.Fatal(c.Visitor())
	}
	c.UserID = ""
	if c.Visitor() != "ip:h" {
		t.Fatal(c.Visitor())
	}
}

func TestHashIPStableAndSalted(t *testing.T) {
	if HashIP("a", "1.1.1.1") != HashIP("a", "1.1.1.1") {
		t.Fatal("not stable")
	}
	if HashIP("a", "1.1.1.1") == HashIP("b", "1.1.1.1") {
		t.Fatal("salt ignored")
	}
}
