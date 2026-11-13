package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// TaskKind is one task type (Task.Type): how its definition is checked and
// expanded into probes, and the few behaviours storage needs to know about.
// Built-in kinds register below; a module adds its own with RegisterTaskKind.
type TaskKind struct {
	Type  string
	Label string // shown in the panel, e.g. 网络探测
	// HasSpec reports whether t carries this kind's definition.
	HasSpec func(Task) bool
	// SpecError is the message when the definition is missing or another
	// kind's definition is present as well.
	SpecError string
	// Validate checks the definition; Task.Validate has already checked the
	// name, that exactly this kind's spec is present, and runs the node checks.
	Validate func(Task) error
	Leaves   func(Task) []Probe
	// FixedBindings: the bound nodes carry per-node roles in the definition,
	// so they change only by editing the task, never by bulk (un)binding.
	FixedBindings bool
	// PrivateTargets: the leaves are not offered in the public target plan.
	PrivateTargets bool
	// PerNodeResults: the kind keeps derived per-node results (the unlock
	// table) that must go when a node stops running the task.
	PerNodeResults bool
}

var taskKinds = struct {
	sync.RWMutex
	byType map[string]TaskKind
	order  []string
}{byType: map[string]TaskKind{}}

// RegisterTaskKind adds a task type. Registering the same type twice, or one
// without HasSpec / Validate / Leaves, is a programming error and panics.
func RegisterTaskKind(k TaskKind) {
	if k.Type == "" || k.HasSpec == nil || k.Validate == nil || k.Leaves == nil {
		panic("model: incomplete task kind " + k.Type)
	}
	taskKinds.Lock()
	defer taskKinds.Unlock()
	if _, dup := taskKinds.byType[k.Type]; dup {
		panic("model: task kind registered twice: " + k.Type)
	}
	taskKinds.byType[k.Type] = k
	taskKinds.order = append(taskKinds.order, k.Type)
}

// LookupTaskKind returns the registered kind for a Task.Type.
func LookupTaskKind(taskType string) (TaskKind, bool) {
	taskKinds.RLock()
	defer taskKinds.RUnlock()
	k, ok := taskKinds.byType[taskType]
	return k, ok
}

// TaskKinds lists the registered kinds in registration order.
func TaskKinds() []TaskKind {
	taskKinds.RLock()
	defer taskKinds.RUnlock()
	out := make([]TaskKind, 0, len(taskKinds.order))
	for _, t := range taskKinds.order {
		out = append(out, taskKinds.byType[t])
	}
	return out
}

// TaskTypes is the list of registered Task.Type values (for API enums).
func TaskTypes() []string {
	out := []string{}
	for _, k := range TaskKinds() {
		out = append(out, k.Type)
	}
	return out
}

func unknownTaskType() error {
	names := []string{}
	for _, k := range TaskKinds() {
		names = append(names, fmt.Sprintf("%s（%s）", k.Type, k.Label))
	}
	list := strings.Join(names, "、")
	if n := len(names); n > 1 {
		list = strings.Join(names[:n-1], "、") + "或" + names[n-1]
	}
	return errors.New("任务类型须为 " + list)
}

func init() {
	RegisterTaskKind(TaskKind{
		Type: TaskProbe, Label: "网络探测",
		HasSpec:   func(t Task) bool { return t.Probe != nil },
		SpecError: "网络探测任务须给 probe（检测内容与目标），不能带 unlock 或 block",
		Validate:  validateProbeTask,
		Leaves:    probeLeaves,
	})
	RegisterTaskKind(TaskKind{
		Type: TaskUnlock, Label: "解锁检测",
		HasSpec:   func(t Task) bool { return t.Unlock != nil },
		SpecError: "解锁检测任务须给 unlock（间隔与协议），不能带 probe 或 block",
		Validate:  func(t Task) error { return t.Leaves()[0].Validate() },
		Leaves: func(t Task) []Probe {
			return []Probe{{ID: t.ID, Name: t.Name, Kind: UnlockKind, Family: t.Unlock.Family, Interval: t.Unlock.Interval}}
		},
		PerNodeResults: true,
	})
	RegisterTaskKind(TaskKind{
		Type: TaskBlock, Label: "IP 被墙检测",
		HasSpec:   func(t Task) bool { return t.Block != nil },
		SpecError: "IP 被墙检测须给 block，不能带 probe 或 unlock",
		Validate:  Task.validateBlock,
		Leaves: func(t Task) []Probe {
			b := t.Block
			// A changed role/interval gets a new key immediately, even while the
			// background purger is still removing the previous definition's jobs.
			data, _ := json.Marshal(b)
			return []Probe{{ID: t.ID, Name: t.Name, Kind: "tcp", Host: b.Host, Port: b.Port, Family: b.Family, Interval: b.Interval, Region: "IP 被墙检测/" + Hash(string(data))[:16], Carrier: "其他"}}
		},
		FixedBindings:  true,
		PrivateTargets: true,
	})
}
