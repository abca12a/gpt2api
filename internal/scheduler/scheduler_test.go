package scheduler

import (
	"reflect"
	"testing"

	"github.com/432539/gpt2api/internal/account"
)

func TestPrioritizeDispatchCandidatesPrefersPlan(t *testing.T) {
	candidates := []*account.Account{
		{ID: 1, PlanType: "plus"},
		{ID: 2, PlanType: "free"},
		{ID: 3, PlanType: "FREE"},
		{ID: 4, PlanType: "team"},
	}

	got := prioritizeDispatchCandidates(candidates, "free")
	ids := make([]uint64, 0, len(got))
	for _, acc := range got {
		ids = append(ids, acc.ID)
	}

	want := []uint64{2, 3, 1, 4}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("prioritized ids = %#v, want %#v", ids, want)
	}
}

func TestDispatchCandidateMatchesRequiredPlan(t *testing.T) {
	if !dispatchCandidateMatchesPlan(&account.Account{PlanType: "FREE"}, "free") {
		t.Fatal("FREE should match required free plan")
	}
	if dispatchCandidateMatchesPlan(&account.Account{PlanType: "plus"}, "free") {
		t.Fatal("plus should not match required free plan")
	}
	if !dispatchCandidateMatchesPlan(&account.Account{PlanType: "plus"}, "") {
		t.Fatal("empty required plan should accept any account")
	}
}
