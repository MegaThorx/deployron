package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// waitForIdle polls until the deployment reports idle or the test times out.
func waitForIdle(t *testing.T, r *runner, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.status(name).State == "idle" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("deployment %q never became idle", name)
}

func TestRunnerCoalescesTriggersIntoOneFollowUp(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int32

	r := newRunner(func(name string) error {
		runs.Add(1)
		started <- struct{}{}
		<-release
		return nil
	})

	first := r.trigger("d")
	<-started // first run is now executing

	second := r.trigger("d")
	third := r.trigger("d")
	fourth := r.trigger("d")

	if second != first+1 {
		t.Errorf("follow-up run ID = %d, want %d", second, first+1)
	}
	if third != second || fourth != second {
		t.Errorf("repeated triggers returned IDs %d, %d; want both %d (coalesced)", third, fourth, second)
	}

	release <- struct{}{} // finish first run; follow-up starts
	<-started
	release <- struct{}{} // finish follow-up
	waitForIdle(t, r, "d")

	if got := runs.Load(); got != 2 {
		t.Errorf("run executed %d times, want 2 (one run + one coalesced follow-up)", got)
	}

	status := r.status("d")
	if status.Last != "success" || status.LastRun != second {
		t.Errorf("status = %+v, want last=success last_run=%d", status, second)
	}
}

func TestRunnerRecordsFailure(t *testing.T) {
	r := newRunner(func(name string) error {
		return errors.New("boom")
	})

	runID := r.trigger("d")
	waitForIdle(t, r, "d")

	status := r.status("d")
	if status.Last != "failed" || status.LastRun != runID {
		t.Errorf("status = %+v, want last=failed last_run=%d", status, runID)
	}
}

func TestRunnerDeploymentsAreIndependent(t *testing.T) {
	block := make(chan struct{})
	r := newRunner(func(name string) error {
		if name == "slow" {
			<-block
		}
		return nil
	})

	r.trigger("slow")
	fast := r.trigger("fast")
	waitForIdle(t, r, "fast")

	if status := r.status("fast"); status.Last != "success" || status.LastRun != fast {
		t.Errorf("fast status = %+v, want last=success last_run=%d", status, fast)
	}
	if status := r.status("slow"); status.State != "running" {
		t.Errorf("slow status = %+v, want state=running", status)
	}

	close(block)
	waitForIdle(t, r, "slow")
}

func TestRunnerStatusBeforeAnyRun(t *testing.T) {
	r := newRunner(func(name string) error { return nil })

	status := r.status("never-ran")
	if status.State != "idle" || status.Last != "none" {
		t.Errorf("status = %+v, want state=idle last=none", status)
	}
}
