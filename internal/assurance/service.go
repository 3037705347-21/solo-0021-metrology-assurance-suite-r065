package assurance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type Service struct {
	repository *repository.Repository
	now        func() time.Time
}

func NewService(repo *repository.Repository) *Service {
	return &Service{
		repository: repo,
		now:        time.Now,
	}
}

func (s *Service) RegisterDevice(ctx context.Context, input contracts.RegisterDeviceRequest) (domain.Device, error) {
	var created domain.Device
	tag := domain.NormalizeAssetTag(input.AssetTag)
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		if _, exists := m.DeviceByTag(tag); exists {
			return fmt.Errorf("%w: %s", domain.ErrDuplicateAssetTag, tag)
		}
		item, err := domain.NewDevice(
			m.NextID("device"),
			input.AssetTag,
			input.Model,
			input.Room,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutDevice(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"device",
			item.ID,
			"",
			"device_enrolled",
			item.Steward,
			"device enrolled for assurance work",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		created = item
		return nil
	})
	return created, err
}

func (s *Service) Device(ctx context.Context, id string) (domain.Device, error) {
	var found domain.Device
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Device(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, id)
		}
		found = item
		return nil
	})
	return found, err
}

func (s *Service) OpenCase(ctx context.Context, input contracts.OpenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		deviceID := strings.TrimSpace(input.DeviceID)
		device, exists := m.Device(deviceID)
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, deviceID)
		}
		if !device.IsActive() {
			return fmt.Errorf("%w: device %s is %s", domain.ErrStateConflict, device.ID, device.Status)
		}
		if active, exists := m.ActiveCaseForDevice(device.ID); exists {
			return fmt.Errorf("%w: case %s is still %s", domain.ErrActiveCaseExists, active.ID, active.State)
		}
		item, err := domain.NewVerificationCase(
			m.NextID("case"),
			device.ID,
			input.Nominal,
			input.Tolerance,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_opened",
			item.Steward,
			"verification case opened",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item, s.now())
		return nil
	})
	return result, err
}

func (s *Service) Case(ctx context.Context, id string) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Case(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		result = caseResponseFromSnapshot(snapshot, item, s.now())
		return nil
	})
	return result, err
}

func (s *Service) ClaimCase(ctx context.Context, caseID string, input contracts.ClaimCaseRequest) (contracts.ClaimResponse, error) {
	var result contracts.ClaimResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if item.State == domain.CaseSealed {
			return fmt.Errorf("%w: case %s is sealed", domain.ErrStateConflict, item.ID)
		}
		now := s.now()
		if claim, exists := m.ClaimConflict(item.ID, now); exists {
			return fmt.Errorf(
				"%w: case %s is held by %s",
				domain.ErrClaimConflict,
				item.ID,
				claim.Holder,
			)
		}
		ttl, err := domain.ClaimTTL(input.TTLSeconds)
		if err != nil {
			return err
		}
		claim, err := domain.NewCaseClaim(
			m.NextID("claim"),
			item.ID,
			input.Holder,
			item.Version,
			now,
			ttl,
		)
		if err != nil {
			return err
		}
		m.DeleteClaimsForCase(item.ID)
		if err := m.PutClaim(claim); err != nil {
			return err
		}
		result = contracts.ClaimResponse{Claim: claim}
		return nil
	})
	return result, err
}

func (s *Service) ReleaseCase(ctx context.Context, caseID string, input contracts.ReleaseCaseRequest) error {
	return s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		if _, exists := m.Case(id); !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		claimID := strings.TrimSpace(input.ClaimID)
		if claimID == "" {
			return fmt.Errorf("%w: claim id is required", domain.ErrInvalidInput)
		}
		if _, exists := m.ResolveClaim(id, claimID); !exists {
			return fmt.Errorf("%w: claim %s", domain.ErrClaimNotFound, claimID)
		}
		return m.DeleteClaim(id, claimID)
	})
}

func (s *Service) consumeClaim(m *repository.Mutation, caseID, claimID, actor string, revision int, at time.Time) error {
	claimID = strings.TrimSpace(claimID)
	if claimID == "" {
		if claim, exists := m.ClaimForCase(caseID); exists && claim.ActiveAt(at) {
			return fmt.Errorf(
				"%w: case %s is held by %s",
				domain.ErrClaimConflict,
				caseID,
				claim.Holder,
			)
		}
		return nil
	}
	claim, exists := m.ResolveClaim(caseID, claimID)
	if !exists {
		return fmt.Errorf(
			"%w: claim %s is not valid for case %s",
			domain.ErrClaimConflict,
			claimID,
			caseID,
		)
	}
	if !claim.ActiveAt(at) {
		return fmt.Errorf("%w: claim %s has expired", domain.ErrClaimConflict, claimID)
	}
	if claim.Holder != strings.TrimSpace(actor) {
		return fmt.Errorf(
			"%w: claim %s is held by %s",
			domain.ErrClaimConflict,
			claimID,
			claim.Holder,
		)
	}
	if claim.Revision != revision {
		return fmt.Errorf(
			"%w: claim %s targets revision %d",
			domain.ErrClaimConflict,
			claimID,
			claim.Revision,
		)
	}
	return m.DeleteClaim(caseID, claim.ID)
}

func (s *Service) RecordMeasurement(ctx context.Context, caseID string, input contracts.RecordMeasurementRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if err := s.consumeClaim(m, item.ID, input.ClaimID, input.Technician, item.Version, s.now()); err != nil {
			return err
		}
		measurement, err := domain.NewMeasurement(
			m.NextID("measurement"),
			item.ID,
			input.Observed,
			item.Nominal,
			item.Tolerance,
			input.Technician,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := item.RecordMeasurement(measurement.Accepted, s.now()); err != nil {
			return err
		}
		if err := m.AppendMeasurement(measurement); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		kind := "measurement_rejected"
		detail := "measurement exceeded tolerance and requires another measurement"
		if measurement.Accepted {
			kind = "measurement_accepted"
			detail = "measurement is within tolerance and ready for review"
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			kind,
			measurement.Technician,
			detail,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item, s.now())
		return nil
	})
	return result, err
}

func (s *Service) ReopenCase(ctx context.Context, caseID string, input contracts.ReopenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if err := s.consumeClaim(m, item.ID, input.ClaimID, input.Actor, item.Version, s.now()); err != nil {
			return err
		}
		if err := item.Reopen(s.now()); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		actor := strings.TrimSpace(input.Actor)
		reason := strings.TrimSpace(input.Reason)
		if actor == "" || reason == "" {
			return fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_reopened",
			actor,
			reason,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item, s.now())
		return nil
	})
	return result, err
}

func (s *Service) SealCase(ctx context.Context, caseID string, input contracts.SealCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if err := s.consumeClaim(m, item.ID, input.ClaimID, input.Reviewer, item.Version, s.now()); err != nil {
			return err
		}
		if _, exists := m.Seal(item.ID); exists {
			return fmt.Errorf("%w: case %s", domain.ErrSealAlreadyCreated, item.ID)
		}
		measurement, ok := bestAcceptedMeasurement(m.Measurements(item.ID))
		if !ok {
			return fmt.Errorf("%w: an accepted measurement is required before sealing", domain.ErrStateConflict)
		}
		if err := item.Seal(s.now()); err != nil {
			return err
		}
		seal, err := domain.NewEvidenceSeal(
			m.NextID("seal"),
			item.ID,
			item.DeviceID,
			measurement.ID,
			input.Reviewer,
			input.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutSeal(seal); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_sealed",
			seal.Reviewer,
			seal.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item, s.now())
		return nil
	})
	return result, err
}

func bestAcceptedMeasurement(items []domain.Measurement) (domain.Measurement, bool) {
	var selected domain.Measurement
	found := false
	for _, item := range items {
		if !item.Accepted {
			continue
		}
		if !found || item.AbsoluteDeviation() < selected.AbsoluteDeviation() {
			selected = item
			found = true
		}
	}
	return selected, found
}

func caseResponse(m *repository.Mutation, item domain.VerificationCase, at time.Time) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: m.Measurements(item.ID),
		Events:       m.Events(item.ID),
	}
	if seal, exists := m.Seal(item.ID); exists {
		result.Seal = &seal
	}
	if claim, exists := m.ClaimForCase(item.ID); exists && claim.ActiveAt(at) {
		result.Claim = &claim
	}
	return result
}

func caseResponseFromSnapshot(snapshot repository.Snapshot, item domain.VerificationCase, at time.Time) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: snapshot.Measurements(item.ID),
		Events:       snapshot.Events(item.ID),
	}
	if seal, exists := snapshot.Seal(item.ID); exists {
		result.Seal = &seal
	}
	if claim, exists := snapshot.Claim(item.ID); exists && claim.ActiveAt(at) {
		result.Claim = &claim
	}
	return result
}
