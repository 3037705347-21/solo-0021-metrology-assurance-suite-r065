package assurance

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestService() (*Service, *testClock) {
	clock := &testClock{now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	service := NewService(repository.New())
	service.now = clock.Now
	return service, clock
}

func openTestCase(t *testing.T, service *Service) string {
	t.Helper()
	device, err := service.RegisterDevice(context.Background(), contracts.RegisterDeviceRequest{
		AssetTag: "MET-9001",
		Model:    "Bench Meter",
		Room:     "Room A",
		Steward:  "Lin",
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	view, err := service.OpenCase(context.Background(), contracts.OpenCaseRequest{
		DeviceID:  device.ID,
		Nominal:   25.0,
		Tolerance: 0.5,
		Steward:   "Qiao",
	})
	if err != nil {
		t.Fatalf("open case: %v", err)
	}
	return view.Case.ID
}

func mustClaim(t *testing.T, service *Service, caseID, holder string, ttlSeconds int) domain.CaseClaim {
	t.Helper()
	view, err := service.ClaimCase(context.Background(), caseID, contracts.ClaimCaseRequest{
		Holder:     holder,
		TTLSeconds: ttlSeconds,
	})
	if err != nil {
		t.Fatalf("claim case as %s: %v", holder, err)
	}
	return view.Claim
}

func recordMeasurement(service *Service, caseID string, observed float64, technician, claimID string) (contracts.CaseResponse, error) {
	return service.RecordMeasurement(context.Background(), caseID, contracts.RecordMeasurementRequest{
		Observed:   observed,
		Technician: technician,
		ClaimID:    claimID,
	})
}

func TestSecondClaimWhileActiveIsRejected(t *testing.T) {
	service, _ := newTestService()
	caseID := openTestCase(t, service)

	mustClaim(t, service, caseID, "Rui", 60)
	_, err := service.ClaimCase(context.Background(), caseID, contracts.ClaimCaseRequest{
		Holder:     "Mira",
		TTLSeconds: 60,
	})
	if !errors.Is(err, domain.ErrClaimConflict) {
		t.Fatalf("second claim error = %v, want claim conflict", err)
	}
}

func TestConcurrentClaimsAllowExactlyOneHolder(t *testing.T) {
	service, _ := newTestService()
	caseID := openTestCase(t, service)

	const attempts = 8
	results := make(chan error, attempts)
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := service.ClaimCase(context.Background(), caseID, contracts.ClaimCaseRequest{
				Holder:     fmt.Sprintf("Tech-%d", index),
				TTLSeconds: 60,
			})
			results <- err
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, domain.ErrClaimConflict) {
			t.Fatalf("losing claim error = %v, want claim conflict", err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent claims succeeded %d times, want exactly 1", successes)
	}
}

func TestClaimCanOnlyBeUsedByItsHolder(t *testing.T) {
	service, _ := newTestService()
	caseID := openTestCase(t, service)
	claim := mustClaim(t, service, caseID, "Rui", 600)

	if _, err := recordMeasurement(service, caseID, 25.2, "Mira", claim.ID); !errors.Is(err, domain.ErrClaimConflict) {
		t.Fatalf("foreign claim use error = %v, want claim conflict", err)
	}

	// The rejected attempt must not consume the claim: the holder can still use it.
	view, err := recordMeasurement(service, caseID, 25.2, "Rui", claim.ID)
	if err != nil {
		t.Fatalf("holder submission: %v", err)
	}
	if view.Case.State != domain.CaseReviewReady {
		t.Fatalf("state = %s, want review_ready", view.Case.State)
	}
}

func TestExpiredClaimCannotSubmitAndCanBeTakenOver(t *testing.T) {
	service, clock := newTestService()
	caseID := openTestCase(t, service)
	claim := mustClaim(t, service, caseID, "Rui", 60)

	clock.Advance(2 * time.Minute)

	if _, err := recordMeasurement(service, caseID, 25.2, "Rui", claim.ID); !errors.Is(err, domain.ErrClaimConflict) {
		t.Fatalf("expired claim submission error = %v, want claim conflict", err)
	}

	// The lapsed claim must not hold the case: another technician takes over.
	successor := mustClaim(t, service, caseID, "Mira", 60)
	view, err := recordMeasurement(service, caseID, 25.2, "Mira", successor.ID)
	if err != nil {
		t.Fatalf("takeover submission: %v", err)
	}
	if view.Case.State != domain.CaseReviewReady {
		t.Fatalf("state = %s, want review_ready", view.Case.State)
	}
}

func TestFailedSubmissionKeepsClaimAndLeavesCaseUntouched(t *testing.T) {
	service, _ := newTestService()
	caseID := openTestCase(t, service)
	claim := mustClaim(t, service, caseID, "Rui", 600)

	before, err := service.Case(context.Background(), caseID)
	if err != nil {
		t.Fatalf("read case: %v", err)
	}

	// A NaN observation is rejected, so nothing may be committed.
	if _, err := recordMeasurement(service, caseID, math.NaN(), "Rui", claim.ID); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid submission error = %v, want invalid input", err)
	}

	after, err := service.Case(context.Background(), caseID)
	if err != nil {
		t.Fatalf("read case: %v", err)
	}
	if after.Case.State != before.Case.State || after.Case.Version != before.Case.Version {
		t.Fatalf("failed submission changed the case: %+v -> %+v", before.Case, after.Case)
	}
	if len(after.Measurements) != len(before.Measurements) || len(after.Events) != len(before.Events) {
		t.Fatalf("failed submission left partial changes: %+v", after)
	}
	if after.Claim == nil || after.Claim.ID != claim.ID {
		t.Fatalf("failed submission ended the claim: %+v", after.Claim)
	}

	// Retrying as-is with the same claim must succeed.
	if _, err := recordMeasurement(service, caseID, 25.2, "Rui", claim.ID); err != nil {
		t.Fatalf("retry with the same claim: %v", err)
	}
}

func TestFailedSealKeepsClaim(t *testing.T) {
	service, _ := newTestService()
	caseID := openTestCase(t, service)
	first := mustClaim(t, service, caseID, "Rui", 600)
	if _, err := recordMeasurement(service, caseID, 25.2, "Rui", first.ID); err != nil {
		t.Fatalf("record measurement: %v", err)
	}

	claim := mustClaim(t, service, caseID, "Vera", 600)
	_, err := service.SealCase(context.Background(), caseID, contracts.SealCaseRequest{
		Reviewer: "Vera",
		Summary:  "",
		ClaimID:  claim.ID,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("seal without summary error = %v, want invalid input", err)
	}

	// The failed seal must not end the claim: retrying as-is succeeds.
	view, err := service.SealCase(context.Background(), caseID, contracts.SealCaseRequest{
		Reviewer: "Vera",
		Summary:  "evidence reviewed",
		ClaimID:  claim.ID,
	})
	if err != nil {
		t.Fatalf("retry seal with the same claim: %v", err)
	}
	if view.Case.State != domain.CaseSealed {
		t.Fatalf("state = %s, want sealed", view.Case.State)
	}
	if view.Claim != nil {
		t.Fatalf("claim lingers after successful seal: %+v", view.Claim)
	}
}

func TestSuccessfulSubmissionConsumesClaim(t *testing.T) {
	service, _ := newTestService()
	caseID := openTestCase(t, service)
	claim := mustClaim(t, service, caseID, "Rui", 600)

	view, err := recordMeasurement(service, caseID, 25.2, "Rui", claim.ID)
	if err != nil {
		t.Fatalf("record measurement: %v", err)
	}
	if view.Claim != nil {
		t.Fatalf("claim still attached after successful submission: %+v", view.Claim)
	}

	// The consumed token cannot be reused.
	if _, err := recordMeasurement(service, caseID, 25.1, "Rui", claim.ID); !errors.Is(err, domain.ErrClaimConflict) {
		t.Fatalf("reused claim error = %v, want claim conflict", err)
	}

	// The case is free: someone else can claim the next handling step.
	mustClaim(t, service, caseID, "Mira", 60)
}

func TestTokenlessSubmissionKeepsLegacyBehavior(t *testing.T) {
	service, clock := newTestService()
	caseID := openTestCase(t, service)

	// No claim anywhere: the legacy tokenless call style keeps working.
	view, err := recordMeasurement(service, caseID, 26.5, "Rui", "")
	if err != nil {
		t.Fatalf("tokenless measurement without any claim: %v", err)
	}
	if view.Case.State != domain.CaseRecheckRequired {
		t.Fatalf("state = %s, want recheck_required", view.Case.State)
	}

	// While a claim is active, tokenless calls are turned away.
	mustClaim(t, service, caseID, "Rui", 60)
	_, err = service.ReopenCase(context.Background(), caseID, contracts.ReopenCaseRequest{
		Actor:  "Lin",
		Reason: "measurement setup was replaced",
	})
	if !errors.Is(err, domain.ErrClaimConflict) {
		t.Fatalf("tokenless reopen during active claim = %v, want claim conflict", err)
	}

	// After the claim lapses, tokenless calls work again.
	clock.Advance(2 * time.Minute)
	reopened, err := service.ReopenCase(context.Background(), caseID, contracts.ReopenCaseRequest{
		Actor:  "Lin",
		Reason: "measurement setup was replaced",
	})
	if err != nil {
		t.Fatalf("tokenless reopen after claim expiry: %v", err)
	}
	if reopened.Case.State != domain.CaseAwaitingMeasurement {
		t.Fatalf("state = %s, want awaiting_measurement", reopened.Case.State)
	}
}

func TestClaimLifecycleAcrossWorkflows(t *testing.T) {
	service, _ := newTestService()
	caseID := openTestCase(t, service)

	outOfTolerance := mustClaim(t, service, caseID, "Rui", 600)
	view, err := recordMeasurement(service, caseID, 26.5, "Rui", outOfTolerance.ID)
	if err != nil {
		t.Fatalf("record out-of-tolerance measurement: %v", err)
	}
	if view.Case.State != domain.CaseRecheckRequired {
		t.Fatalf("state = %s, want recheck_required", view.Case.State)
	}

	rework := mustClaim(t, service, caseID, "Lin", 600)
	view, err = service.ReopenCase(context.Background(), caseID, contracts.ReopenCaseRequest{
		Actor:   "Lin",
		Reason:  "measurement setup was replaced",
		ClaimID: rework.ID,
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if view.Case.State != domain.CaseAwaitingMeasurement {
		t.Fatalf("state = %s, want awaiting_measurement", view.Case.State)
	}

	inTolerance := mustClaim(t, service, caseID, "Rui", 600)
	view, err = recordMeasurement(service, caseID, 25.2, "Rui", inTolerance.ID)
	if err != nil {
		t.Fatalf("record in-tolerance measurement: %v", err)
	}
	if view.Case.State != domain.CaseReviewReady {
		t.Fatalf("state = %s, want review_ready", view.Case.State)
	}

	review := mustClaim(t, service, caseID, "Vera", 600)
	view, err = service.SealCase(context.Background(), caseID, contracts.SealCaseRequest{
		Reviewer: "Vera",
		Summary:  "accepted measurement sealed for review",
		ClaimID:  review.ID,
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if view.Case.State != domain.CaseSealed {
		t.Fatalf("state = %s, want sealed", view.Case.State)
	}
	if view.Claim != nil {
		t.Fatalf("claim lingers after seal: %+v", view.Claim)
	}

	// A sealed case cannot be claimed again.
	_, err = service.ClaimCase(context.Background(), caseID, contracts.ClaimCaseRequest{Holder: "Mira"})
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("claim on sealed case = %v, want state conflict", err)
	}
}

func TestStaleClaimCannotSubmitAfterCaseMovedOn(t *testing.T) {
	service, clock := newTestService()
	caseID := openTestCase(t, service)

	stale := mustClaim(t, service, caseID, "Rui", 60)
	clock.Advance(2 * time.Minute)

	// The case moved on without the stale claim: a tokenless measurement
	// bumps the case revision, so the stale claim targets an old revision.
	if _, err := recordMeasurement(service, caseID, 26.5, "Mira", ""); err != nil {
		t.Fatalf("tokenless measurement after claim expiry: %v", err)
	}
	clock.Advance(-2 * time.Minute) // pretend the stale claim is still in its window
	if _, err := recordMeasurement(service, caseID, 25.2, "Rui", stale.ID); !errors.Is(err, domain.ErrClaimConflict) {
		t.Fatalf("stale claim submission error = %v, want claim conflict", err)
	}
}
