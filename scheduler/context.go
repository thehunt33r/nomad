package scheduler

import (
	"log"
	"regexp"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/nomad/nomad/structs"
)

// Context is used to track contextual information used for placement
type Context interface {
	// State is used to inspect the current global state
	State() State

	// Plan returns the current plan
	Plan() *structs.Plan

	// Logger provides a way to log
	Logger() *log.Logger

	// Metrics returns the current metrics
	Metrics() *structs.AllocMetric

	// Reset is invoked after making a placement
	Reset()

	// ProposedAllocs returns the proposed allocations for a node
	// which is the existing allocations, removing evictions, and
	// adding any planned placements.
	ProposedAllocs(nodeID string) ([]*structs.Allocation, error)

	// RegexpCache is a cache of regular expressions
	RegexpCache() map[string]*regexp.Regexp

	// ConstraintCache is a cache of version constraints
	ConstraintCache() map[string]version.Constraints

	// Eligibility returns a tracker for node eligibility in the context of the
	// eval.
	Eligibility() *EvalEligibility
}

// EvalCache is used to cache certain things during an evaluation
type EvalCache struct {
	reCache         map[string]*regexp.Regexp
	constraintCache map[string]version.Constraints
}

func (e *EvalCache) RegexpCache() map[string]*regexp.Regexp {
	if e.reCache == nil {
		e.reCache = make(map[string]*regexp.Regexp)
	}
	return e.reCache
}
func (e *EvalCache) ConstraintCache() map[string]version.Constraints {
	if e.constraintCache == nil {
		e.constraintCache = make(map[string]version.Constraints)
	}
	return e.constraintCache
}

// EvalContext is a Context used during an Evaluation
type EvalContext struct {
	EvalCache
	state       State
	plan        *structs.Plan
	logger      *log.Logger
	metrics     *structs.AllocMetric
	eligibility *EvalEligibility
}

// NewEvalContext constructs a new EvalContext
func NewEvalContext(s State, p *structs.Plan, log *log.Logger) *EvalContext {
	ctx := &EvalContext{
		state:   s,
		plan:    p,
		logger:  log,
		metrics: new(structs.AllocMetric),
	}
	return ctx
}

func (e *EvalContext) State() State {
	return e.state
}

func (e *EvalContext) Plan() *structs.Plan {
	return e.plan
}

func (e *EvalContext) Logger() *log.Logger {
	return e.logger
}

func (e *EvalContext) Metrics() *structs.AllocMetric {
	return e.metrics
}

func (e *EvalContext) SetState(s State) {
	e.state = s
}

func (e *EvalContext) Reset() {
	e.metrics = new(structs.AllocMetric)
}

func (e *EvalContext) ProposedAllocs(nodeID string) ([]*structs.Allocation, error) {
	// Get the existing allocations
	existingAlloc, err := e.state.AllocsByNode(nodeID)
	if err != nil {
		return nil, err
	}

	// Filter on alloc state
	existingAlloc = structs.FilterTerminalAllocs(existingAlloc)

	// Determine the proposed allocation by first removing allocations
	// that are planned evictions and adding the new allocations.
	proposed := existingAlloc
	if update := e.plan.NodeUpdate[nodeID]; len(update) > 0 {
		proposed = structs.RemoveAllocs(existingAlloc, update)
	}
	proposed = append(proposed, e.plan.NodeAllocation[nodeID]...)

	// Ensure the return is not nil
	if proposed == nil {
		proposed = make([]*structs.Allocation, 0)
	}
	return proposed, nil
}

func (e *EvalContext) Eligibility() *EvalEligibility {
	if e.eligibility == nil {
		e.eligibility = NewEvalEligibility()
	}

	return e.eligibility
}

type ComputedClassFeasibility byte

const (
	// EvalComputedClassUnknown is the initial state until the eligibility has
	// been explicitely marked to eligible/ineligible or escaped.
	EvalComputedClassUnknown ComputedClassFeasibility = iota

	// EvalComputedClassIneligible is used to mark the computed class as
	// ineligible for the evaluation.
	EvalComputedClassIneligible

	// EvalComputedClassIneligible is used to mark the computed class as
	// eligible for the evaluation.
	EvalComputedClassEligible

	// EvalComputedClassEscaped signals that computed class can not determine
	// eligibility because a constraint exists that is not captured by computed
	// node classes.
	EvalComputedClassEscaped
)

// EvalEligibility tracks eligibility of nodes by computed node class over the
// course of an evaluation.
type EvalEligibility struct {
	// job tracks the eligibility at the job level per computed node class.
	job map[uint64]ComputedClassFeasibility

	// jobEscapedConstraints tracks escaped constraints at the job level.
	jobEscapedConstraints []*structs.Constraint

	// taskGroups tracks the eligibility at the task group level per computed
	// node class.
	taskGroups map[string]map[uint64]ComputedClassFeasibility

	// tgEscapedConstraints is a map of task groups to a set of constraints that
	// have escaped.
	tgEscapedConstraints map[string][]*structs.Constraint
}

// NewEvalEligibility returns an eligibility tracker for the context of an evaluation.
func NewEvalEligibility() *EvalEligibility {
	return &EvalEligibility{
		job:                  make(map[uint64]ComputedClassFeasibility),
		taskGroups:           make(map[string]map[uint64]ComputedClassFeasibility),
		tgEscapedConstraints: make(map[string][]*structs.Constraint),
	}
}

// SetJob takes the job being evaluated and calculates the escaped constraints
// at the job and task group level.
func (e *EvalEligibility) SetJob(job *structs.Job) {
	// Determine the escaped constraints for the job.
	e.jobEscapedConstraints = structs.EscapedConstraints(job.Constraints)

	// Determine the escaped constraints per task group.
	for _, tg := range job.TaskGroups {
		constraints := tg.Constraints
		for _, task := range tg.Tasks {
			constraints = append(constraints, task.Constraints...)
		}

		e.tgEscapedConstraints[tg.Name] = structs.EscapedConstraints(constraints)
	}
}

// JobStatus returns the eligibility status of the job.
func (e *EvalEligibility) JobStatus(class uint64) ComputedClassFeasibility {
	// COMPAT: Computed node class was introduced in 0.3. Clients running < 0.3
	// will not have a computed class. The safest value to return is the escaped
	// case, since it disables any optimization.
	if len(e.jobEscapedConstraints) != 0 || class == 0 {
		return EvalComputedClassEscaped
	}

	if status, ok := e.job[class]; ok {
		return status
	}
	return EvalComputedClassUnknown
}

// SetJobEligibility sets the eligibility status of the job for the computed
// node class.
func (e *EvalEligibility) SetJobEligibility(eligible bool, class uint64) {
	if eligible {
		e.job[class] = EvalComputedClassEligible
	} else {
		e.job[class] = EvalComputedClassIneligible
	}
}

// TaskGroupStatus returns the eligibility status of the task group.
func (e *EvalEligibility) TaskGroupStatus(tg string, class uint64) ComputedClassFeasibility {
	// COMPAT: Computed node class was introduced in 0.3. Clients running < 0.3
	// will not have a computed class. The safest value to return is the escaped
	// case, since it disables any optimization.
	if class == 0 {
		return EvalComputedClassEscaped
	}

	if escaped, ok := e.tgEscapedConstraints[tg]; ok {
		if len(escaped) != 0 {
			return EvalComputedClassEscaped
		}
	}

	if classes, ok := e.taskGroups[tg]; ok {
		if status, ok := classes[class]; ok {
			return status
		}
	}
	return EvalComputedClassUnknown
}

// SetTaskGroupEligibility sets the eligibility status of the task group for the
// computed node class.
func (e *EvalEligibility) SetTaskGroupEligibility(eligible bool, tg string, class uint64) {
	var eligibility ComputedClassFeasibility
	if eligible {
		eligibility = EvalComputedClassEligible
	} else {
		eligibility = EvalComputedClassIneligible
	}

	if classes, ok := e.taskGroups[tg]; ok {
		classes[class] = eligibility
	} else {
		e.taskGroups[tg] = map[uint64]ComputedClassFeasibility{class: eligibility}
	}
}
