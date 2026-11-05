package ipsentinel

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"time"
	_ "time/tzdata"
)

type ScheduleSlot struct {
	Time  string `json:"time"`
	Phase string `json:"phase"`
}

func (slot ScheduleSlot) Purpose() string {
	if slot.Phase == PhaseExperiment {
		return PurposeExperiment
	}
	return PurposeObserve
}

type ScheduleSpec struct {
	Enabled            bool           `json:"enabled"`
	Timezone           string         `json:"timezone"`
	Config             ConfigRef      `json:"config"`
	ObserveServices    []string       `json:"observe_services"`
	ExperimentServices []string       `json:"experiment_services"`
	Slots              []ScheduleSlot `json:"slots"`
	TimeoutMS          int64          `json:"timeout_ms"`
}

type ScheduleInput struct {
	ScheduleSpec
	ExpectedRevision int64 `json:"expected_revision"`
}

type Schedule struct {
	ScheduleSpec
	ID            string      `json:"id"`
	Revision      int64       `json:"revision"`
	Binding       Binding     `json:"binding"`
	DailyBudget   DailyLimits `json:"daily_budget"`
	NetworkPerRun int64       `json:"network_per_run"`
}

type ScheduleCoverage struct {
	ServiceID    string   `json:"service_id"`
	Purpose      string   `json:"purpose"`
	MaxGapHours  *float64 `json:"max_gap_hours"`
	Sufficient48 bool     `json:"sufficient_48h"`
}

type ScheduledRun struct {
	LocalDate    string `json:"local_date"`
	LocalTime    string `json:"local_time"`
	Timezone     string `json:"timezone"`
	ServiceID    string `json:"service_id"`
	Purpose      string `json:"purpose"`
	Phase        string `json:"phase"`
	JobID        string `json:"job_id"`
	ActionID     string `json:"action_id,omitempty"`
	DueAt        int64  `json:"due_at"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	ActionStatus string `json:"action_status,omitempty"`
	RunState     string `json:"run_state,omitempty"`
}

type ScheduleView struct {
	Schedule *Schedule          `json:"schedule"`
	Coverage []ScheduleCoverage `json:"coverage"`
	Recent   []ScheduledRun     `json:"recent"`
}

func (spec ScheduleSpec) Validate() error {
	if spec.Timezone == "" || spec.Timezone == "Local" {
		return semanticErr("请明确填写节点 IANA 时区")
	}
	if _, err := time.LoadLocation(spec.Timezone); err != nil {
		return semanticErr("节点时区无效")
	}
	raw, err := json.Marshal(spec.Config)
	if err != nil {
		return err
	}
	if err := ValidateDefinition("config_ref", raw, nil); err != nil {
		return semanticErr("需要已应用的配置引用")
	}
	if spec.TimeoutMS < 1000 || spec.TimeoutMS > 900000 || len(spec.Slots) == 0 || len(spec.Slots) > 32 {
		return semanticErr("需要 1–32 个时段和有效超时")
	}
	for _, services := range [][]string{spec.ObserveServices, spec.ExperimentServices} {
		if len(services) > 32 {
			return semanticErr("每种用途最多 32 个服务")
		}
		seen := map[string]bool{}
		for _, service := range services {
			if service == "" || len(service) > 64 || seen[service] {
				return semanticErr("服务 ID 不能为空或重复")
			}
			seen[service] = true
		}
	}
	seen := map[string]bool{}
	for _, slot := range spec.Slots {
		parsed, err := time.Parse("15:04", slot.Time)
		if err != nil || parsed.Format("15:04") != slot.Time || seen[slot.Time] {
			return semanticErr("时段须为不重复的 HH:MM")
		}
		if !slices.Contains([]string{PhaseBaseline, PhaseFollowup, PhaseExperiment}, slot.Phase) {
			return semanticErr("时段阶段无效")
		}
		if len(spec.Services(slot.Purpose())) == 0 {
			return semanticErr("时段用途没有对应服务")
		}
		seen[slot.Time] = true
	}
	for _, purpose := range []string{PurposeObserve, PurposeExperiment} {
		if len(spec.Services(purpose)) > 0 && !slices.ContainsFunc(spec.Slots, func(slot ScheduleSlot) bool { return slot.Purpose() == purpose }) {
			return semanticErr("服务用途缺少时段")
		}
	}
	return nil
}

func (spec ScheduleSpec) Services(purpose string) []string {
	if purpose == PurposeExperiment {
		return spec.ExperimentServices
	}
	return spec.ObserveServices
}

func (spec ScheduleSpec) SortedSlots() []ScheduleSlot {
	slots := slices.Clone(spec.Slots)
	slices.SortFunc(slots, func(left, right ScheduleSlot) int {
		if left.Time < right.Time {
			return -1
		}
		if left.Time > right.Time {
			return 1
		}
		return 0
	})
	return slots
}

func (spec ScheduleSpec) ServiceForSlot(local time.Time, slot ScheduleSlot) string {
	services := spec.Services(slot.Purpose())
	if len(services) == 0 {
		return ""
	}
	var count, ordinal int64
	for _, candidate := range spec.SortedSlots() {
		if candidate.Purpose() == slot.Purpose() {
			if candidate.Time < slot.Time {
				ordinal++
			}
			count++
		}
	}
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC).Unix() / 86400
	index := (day*count + ordinal) % int64(len(services))
	if index < 0 {
		index += int64(len(services))
	}
	return services[index]
}

func ScheduledSlotTime(local time.Time, slot ScheduleSlot) (time.Time, bool) {
	clock, err := time.Parse("15:04", slot.Time)
	if err != nil {
		return time.Time{}, false
	}
	due := time.Date(local.Year(), local.Month(), local.Day(), clock.Hour(), clock.Minute(), 0, 0, local.Location())
	expected := local.Format("2006-01-02") + " " + slot.Time
	if due.Format("2006-01-02 15:04") != expected {
		return time.Time{}, false
	}
	_, offset := due.Zone()
	first := due
	for _, adjacent := range []time.Time{due.Add(-24 * time.Hour), due.Add(24 * time.Hour)} {
		_, otherOffset := adjacent.Zone()
		candidate := due.Add(time.Duration(offset-otherOffset) * time.Second)
		if candidate.Before(first) && candidate.Format("2006-01-02 15:04") == expected {
			first = candidate
		}
	}
	return first, true
}

func ScheduledJobID(node string, binding Binding, localDate, localTime string) string {
	raw, _ := json.Marshal([]any{node, binding, localDate, localTime})
	digest := sha256.Sum256(raw)
	digest[6] = (digest[6] & 0x0f) | 0x50
	digest[8] = (digest[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
}

func (spec ScheduleSpec) Coverage(now time.Time) []ScheduleCoverage {
	coverage := []ScheduleCoverage{}
	location, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return coverage
	}
	local := now.In(location)
	firstDay := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, location)
	for _, purpose := range []string{PurposeObserve, PurposeExperiment} {
		services := spec.Services(purpose)
		last := map[string]time.Time{}
		gaps := map[string]time.Duration{}
		for day := 0; day < 370+2*len(services); day++ {
			date := firstDay.AddDate(0, 0, day)
			for _, slot := range spec.SortedSlots() {
				if slot.Purpose() != purpose {
					continue
				}
				due, exists := ScheduledSlotTime(date, slot)
				if !exists {
					continue
				}
				service := spec.ServiceForSlot(date, slot)
				if previous, ok := last[service]; ok && due.Sub(previous) > gaps[service] {
					gaps[service] = due.Sub(previous)
				}
				last[service] = due
			}
		}
		for _, service := range services {
			entry := ScheduleCoverage{ServiceID: service, Purpose: purpose}
			if gap := gaps[service]; gap > 0 {
				hours := gap.Hours()
				entry.MaxGapHours, entry.Sufficient48 = &hours, hours <= 48
			}
			coverage = append(coverage, entry)
		}
	}
	return coverage
}
