package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
)

func TestFailedMutationRollsBackClaimChanges(t *testing.T) {
	repo := New()
	claim, err := domain.NewCaseClaim("claim-0001", "case-0001", "Rui", 1, time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("build claim: %v", err)
	}
	if err := repo.Mutate(context.Background(), func(m *Mutation) error {
		return m.PutClaim(claim)
	}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	// A mutation that deletes the claim and then fails must commit nothing.
	boom := errors.New("boom")
	if err := repo.Mutate(context.Background(), func(m *Mutation) error {
		if err := m.DeleteClaim(claim.CaseID, claim.ID); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("mutation error = %v, want %v", err, boom)
	}

	if err := repo.Read(context.Background(), func(snapshot Snapshot) error {
		if _, exists := snapshot.Claim(claim.CaseID); !exists {
			return errors.New("claim was lost by a failed mutation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
