// Package event defines the click event and its validation rules.
package event

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Click is the message stored in Kafka (JSON).
type Click struct {
	EventID      string    `json:"event_id"`
	AdID         string    `json:"ad_id"`
	CampaignID   string    `json:"campaign_id"`
	AdvertiserID string    `json:"advertiser_id"`
	UserID       string    `json:"user_id,omitempty"`
	ClickedAt    time.Time `json:"clicked_at"`
	Country      string    `json:"country"`
	DeviceType   string    `json:"device_type"`
	Browser      string    `json:"browser"`
	Source       string    `json:"source"`
	IPHash       string    `json:"ip_hash"`
}

// PartitionKey decides ordering and load spread in Kafka.
// ad_id: all clicks of one ad land on one partition, so a consumer sees them in
// order and batches updates to the same aggregate row together (fewer DB lock
// conflicts). Trade-off: one extremely hot ad can overload one partition.
func (c Click) PartitionKey() []byte { return []byte(c.AdID) }

// Visitor identifies who clicked, for unique-click counting. It prefers the
// pseudonymous user id and falls back to the IP hash.
func (c Click) Visitor() string {
	if c.UserID != "" {
		return "u:" + c.UserID
	}
	return "ip:" + c.IPHash
}

var ErrInvalid = errors.New("invalid click event")

var devices = map[string]bool{"desktop": true, "mobile": true, "tablet": true, "other": true}

// Limits controls time-based validation.
type Limits struct {
	MaxAge     time.Duration
	FutureSkew time.Duration
}

// Validate returns an error wrapping ErrInvalid. Errors are permanent: retrying
// the same bytes can never succeed, so callers send them to the DLQ.
func (c Click) Validate(now time.Time, l Limits) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
	}
	if _, err := uuid.Parse(c.EventID); err != nil {
		return bad("event_id is not a UUID")
	}
	if c.AdID == "" || len(c.AdID) > 64 {
		return bad("ad_id missing or too long")
	}
	if len(c.Country) != 2 || c.Country[0] < 'A' || c.Country[0] > 'Z' || c.Country[1] < 'A' || c.Country[1] > 'Z' {
		return bad("country must be ISO-3166 alpha-2 uppercase")
	}
	if !devices[c.DeviceType] {
		return bad("device_type must be desktop|mobile|tablet|other")
	}
	if c.Browser == "" || len(c.Browser) > 64 {
		return bad("browser missing or too long")
	}
	if c.Source == "" || len(c.Source) > 64 {
		return bad("source missing or too long")
	}
	if c.IPHash == "" {
		return bad("ip_hash missing")
	}
	if c.ClickedAt.IsZero() {
		return bad("clicked_at missing")
	}
	if now.Sub(c.ClickedAt) > l.MaxAge {
		return bad("clicked_at older than %s", l.MaxAge)
	}
	if c.ClickedAt.Sub(now) > l.FutureSkew {
		return bad("clicked_at is in the future")
	}
	return nil
}

// HashIP returns a salted SHA-256 so raw IPs are never stored.
func HashIP(salt, ip string) string {
	sum := sha256.Sum256([]byte(salt + "|" + ip))
	return hex.EncodeToString(sum[:16])
}

// NewID returns a UUIDv7 (time-ordered, index friendly).
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}
