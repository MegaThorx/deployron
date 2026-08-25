package main

import (
	"sync"
	"time"

	"github.com/MegaThorx/deployron/common"
)

type runResult struct {
	runID      uint64
	success    bool
	finishedAt time.Time
	duration   time.Duration
}

type runState struct {
	running   bool
	runningID uint64
	// While a run is in progress, at most one follow-up is queued; further
	// triggers coalesce into it so nothing piles up but the latest code is
	// never missed.
	pending   bool
	pendingID uint64
	nextID    uint64
	last      *runResult
}

// runner serializes runs per deployment and tracks their outcomes. The actual
// script execution is injected so the state machine is testable.
type runner struct {
	mu     sync.Mutex
	states map[string]*runState
	run    func(name string) error
}

func newRunner(run func(name string) error) *runner {
	return &runner{states: make(map[string]*runState), run: run}
}

func (r *runner) state(name string) *runState {
	st, ok := r.states[name]
	if !ok {
		st = &runState{}
		r.states[name] = st
	}
	return st
}

// trigger requests a run of the named deployment and returns the ID of the
// run that will carry this trigger. It never blocks on the run itself.
func (r *runner) trigger(name string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	st := r.state(name)

	if st.running {
		if !st.pending {
			st.pending = true
			st.nextID++
			st.pendingID = st.nextID
		}
		return st.pendingID
	}

	st.nextID++
	st.running = true
	st.runningID = st.nextID
	go r.execute(name, st.runningID)
	return st.runningID
}

func (r *runner) execute(name string, runID uint64) {
	for {
		start := time.Now()
		err := r.run(name)

		r.mu.Lock()
		st := r.state(name)
		st.last = &runResult{
			runID:      runID,
			success:    err == nil,
			finishedAt: time.Now(),
			duration:   time.Since(start),
		}
		if st.pending {
			st.pending = false
			runID = st.pendingID
			st.runningID = runID
			r.mu.Unlock()
			continue
		}
		st.running = false
		r.mu.Unlock()
		return
	}
}

func (r *runner) status(name string) common.DeployStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	st := r.state(name)

	status := common.DeployStatus{State: "idle", Last: "none"}
	if st.running {
		status.State = "running"
		status.Run = st.runningID
	}
	if st.last != nil {
		status.Last = "failed"
		if st.last.success {
			status.Last = "success"
		}
		status.LastRun = st.last.runID
		status.FinishedAt = st.last.finishedAt.Unix()
		status.DurationMS = st.last.duration.Milliseconds()
	}
	return status
}
