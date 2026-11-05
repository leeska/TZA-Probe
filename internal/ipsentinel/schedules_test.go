package ipsentinel

import (
	"slices"
	"testing"
	"time"
)

func scheduleSpecFixture() ScheduleSpec {
	return ScheduleSpec{
		Enabled: true, Timezone: "UTC", Config: ConfigRef{Revision: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		ObserveServices: []string{"google", "meta", "third"}, ExperimentServices: []string{"google"},
		Slots: []ScheduleSlot{{Time: "08:00", Phase: PhaseFollowup}, {Time: "20:00", Phase: PhaseExperiment}}, TimeoutMS: 30000,
	}
}

func TestScheduleValidationAndIndependentServiceRotation(t *testing.T) {
	spec := scheduleSpecFixture()
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for day := 1; day <= 3; day++ {
		date := time.Date(2026, 11, day, 0, 0, 0, 0, time.UTC)
		seen[spec.ServiceForSlot(date, spec.Slots[0])] = true
		if spec.ServiceForSlot(date, spec.Slots[1]) != "google" {
			t.Fatal("experiment rotation used observe service list")
		}
	}
	if len(seen) != 3 {
		t.Fatal("daily slots did not rotate through services", seen)
	}
	for _, mutate := range []func(*ScheduleSpec){
		func(spec *ScheduleSpec) { spec.Timezone = "Local" },
		func(spec *ScheduleSpec) { spec.Timezone = "not/a/timezone" },
		func(spec *ScheduleSpec) { spec.Slots[0].Time = "8:00" },
		func(spec *ScheduleSpec) { spec.Slots[0].Phase = "heal" },
		func(spec *ScheduleSpec) { spec.Slots[1].Time = spec.Slots[0].Time },
		func(spec *ScheduleSpec) { spec.ObserveServices = nil },
		func(spec *ScheduleSpec) { spec.ExperimentServices = []string{"google", "google"} },
		func(spec *ScheduleSpec) { spec.Config.SHA256 = "" },
	} {
		invalid := scheduleSpecFixture()
		mutate(&invalid)
		if invalid.Validate() == nil {
			t.Fatal("invalid schedule accepted", invalid)
		}
	}
}

func TestScheduledSlotDSTAndDeterministicJob(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	spring := time.Date(2026, 3, 8, 12, 0, 0, 0, location)
	if _, exists := ScheduledSlotTime(spring, ScheduleSlot{Time: "02:30"}); exists {
		t.Fatal("nonexistent spring-forward time was shifted into a new slot")
	}
	fall := time.Date(2026, 11, 1, 12, 0, 0, 0, location)
	first, exists := ScheduledSlotTime(fall, ScheduleSlot{Time: "01:30"})
	if !exists || first.UTC().Format(time.RFC3339) != "2026-11-01T05:30:00Z" {
		t.Fatal("fold did not choose first occurrence", first)
	}
	binding := Binding{ExecutorInstanceID: NewUUID(), Epoch: 1}
	job := ScheduledJobID("node", binding, "2026-11-01", "01:30")
	if !ValidUUID(job) || job != ScheduledJobID("node", binding, "2026-11-01", "01:30") {
		t.Fatal("unstable logical job", job)
	}
	binding.Epoch++
	if job == ScheduledJobID("node", binding, "2026-11-01", "01:30") {
		t.Fatal("new authorization reused old job identity")
	}
}

func TestScheduleCoverageIncludesDSTAndServiceBudgetDilution(t *testing.T) {
	spec := scheduleSpecFixture()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	coverage := spec.Coverage(now)
	if len(coverage) != 4 || coverage[0].MaxGapHours == nil || *coverage[0].MaxGapHours != 72 || coverage[0].Sufficient48 {
		t.Fatal("three services received a multiplied daily budget", coverage)
	}
	if entry := coverage[3]; entry.Purpose != PurposeExperiment || !entry.Sufficient48 || *entry.MaxGapHours != 24 {
		t.Fatal("experiment coverage mixed with observe", entry)
	}
	spec.Timezone = "America/New_York"
	spec.ObserveServices = slices.Clone(spec.ObserveServices[:2])
	coverage = spec.Coverage(now)
	if coverage[0].MaxGapHours == nil || *coverage[0].MaxGapHours != 49 || coverage[0].Sufficient48 {
		t.Fatal("DST 49-hour gap advertised as 48-hour coverage", coverage)
	}
}
