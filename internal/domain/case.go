package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type CaseState string

const (
	CaseAwaitingMeasurement CaseState = "awaiting_measurement"
	CaseReviewReady         CaseState = "review_ready"
	CaseRecheckRequired     CaseState = "recheck_required"
	CaseSealed              CaseState = "sealed"
)

type VerificationCase struct {
	ID        string     `json:"id"`
	DeviceID  string     `json:"device_id"`
	Nominal   float64    `json:"nominal"`
	Tolerance float64    `json:"tolerance"`
	Steward   string     `json:"steward"`
	State     CaseState  `json:"state"`
	Version   int        `json:"version"`
	OpenedAt  time.Time  `json:"opened_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	SealedAt  *time.Time `json:"sealed_at,omitempty"`
}

func NewVerificationCase(id, deviceID string, nominal, tolerance float64, steward string, at time.Time) (VerificationCase, error) {
	item := VerificationCase{
		ID:        strings.TrimSpace(id),
		DeviceID:  strings.TrimSpace(deviceID),
		Nominal:   nominal,
		Tolerance: tolerance,
		Steward:   strings.TrimSpace(steward),
		State:     CaseAwaitingMeasurement,
		Version:   1,
		OpenedAt:  at.UTC(),
		UpdatedAt: at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return VerificationCase{}, err
	}
	return item, nil
}

func (c VerificationCase) Validate() error {
	if c.ID == "" || c.DeviceID == "" || c.Steward == "" {
		return fmt.Errorf("%w: case id, device id, and steward are required", ErrInvalidInput)
	}
	if !isFinite(c.Nominal) || !isFinite(c.Tolerance) || c.Tolerance <= 0 {
		return fmt.Errorf("%w: nominal must be finite and tolerance must be positive", ErrInvalidInput)
	}
	if c.Version < 1 || c.OpenedAt.IsZero() || c.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: case version and timestamps are required", ErrInvalidInput)
	}
	switch c.State {
	case CaseAwaitingMeasurement, CaseReviewReady, CaseRecheckRequired, CaseSealed:
		return nil
	default:
		return fmt.Errorf("%w: unsupported case state %q", ErrInvalidInput, c.State)
	}
}

func (c VerificationCase) IsUnfinished() bool {
	return c.State != CaseSealed
}

func (c *VerificationCase) RecordMeasurement(accepted bool, at time.Time) error {
	if c.State != CaseAwaitingMeasurement {
		return fmt.Errorf("%w: case is not awaiting measurement", ErrStateConflict)
	}
	if accepted {
		c.State = CaseReviewReady
	} else {
		c.State = CaseRecheckRequired
	}
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

func (c *VerificationCase) Reopen(at time.Time) error {
	if c.State != CaseRecheckRequired {
		return fmt.Errorf("%w: only a recheck_required case can be reopened", ErrStateConflict)
	}
	c.State = CaseAwaitingMeasurement
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

func (c *VerificationCase) Seal(at time.Time) error {
	if c.State != CaseReviewReady {
		return fmt.Errorf("%w: case is not ready for review", ErrStateConflict)
	}
	stamp := at.UTC()
	c.State = CaseSealed
	c.Version++
	c.UpdatedAt = stamp
	c.SealedAt = &stamp
	return nil
}

const (
	DefaultClaimTTL = 15 * time.Minute
	MinClaimTTL     = time.Second
	MaxClaimTTL     = time.Hour
)

type CaseClaim struct {
	ID         string    `json:"id"`
	CaseID     string    `json:"case_id"`
	Holder     string    `json:"holder"`
	Revision   int       `json:"revision"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func NewCaseClaim(id, caseID, holder string, revision int, at time.Time, ttl time.Duration) (CaseClaim, error) {
	item := CaseClaim{
		ID:         strings.TrimSpace(id),
		CaseID:     strings.TrimSpace(caseID),
		Holder:     strings.TrimSpace(holder),
		Revision:   revision,
		AcquiredAt: at.UTC(),
		ExpiresAt:  at.UTC().Add(ttl),
	}
	if err := item.Validate(); err != nil {
		return CaseClaim{}, err
	}
	return item, nil
}

func (c CaseClaim) Validate() error {
	if c.ID == "" || c.CaseID == "" || c.Holder == "" {
		return fmt.Errorf("%w: claim id, case id, and holder are required", ErrInvalidInput)
	}
	if c.Revision < 1 {
		return fmt.Errorf("%w: claim revision must be positive", ErrInvalidInput)
	}
	if c.AcquiredAt.IsZero() || !c.ExpiresAt.After(c.AcquiredAt) {
		return fmt.Errorf("%w: claim validity window is required", ErrInvalidInput)
	}
	return nil
}

func (c CaseClaim) IsExpired(at time.Time) bool {
	return !at.UTC().Before(c.ExpiresAt)
}

func (c CaseClaim) ActiveAt(at time.Time) bool {
	return !c.ExpiresAt.IsZero()
}

func ClaimTTL(seconds int) (time.Duration, error) {
	if seconds <= 0 {
		return DefaultClaimTTL, nil
	}
	ttl := time.Duration(seconds) * time.Second
	if ttl < MinClaimTTL || ttl > MaxClaimTTL {
		return 0, fmt.Errorf(
			"%w: claim ttl must be between %s and %s",
			ErrInvalidInput,
			MinClaimTTL,
			MaxClaimTTL,
		)
	}
	return ttl, nil
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
