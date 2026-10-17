package cognition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

var repairRetryDelays = [...]time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute}

func repairInputDigest(raw []byte, existed bool) string {
	hash := sha256.New()
	if existed {
		_, _ = hash.Write([]byte{1})
	} else {
		_, _ = hash.Write([]byte{0})
	}
	_, _ = hash.Write(raw)
	return hex.EncodeToString(hash.Sum(nil))
}

func repairFailureResult(jobType string, spec Spec) Result {
	if !spec.repair {
		return Result{}
	}
	snapshot := spec.FileSnapshots[memoryRuntimePrefix+spec.repairTarget.Path]
	return Result{Type: jobType, Repair: true, CheckpointSeq: spec.CheckpointSeq,
		repairTarget: spec.repairTarget.Path, repairDigest: repairInputDigest(snapshot.Content, snapshot.Existed)}
}

func updateRepairSchedule(state *JobState, result Result, runErr error, finished time.Time) {
	state.RepairPending = false
	state.RepairRetryAt = time.Time{}
	if !result.Repair {
		state.RepairFailures, state.RepairStopped = 0, false
		return
	}
	state.RepairTarget, state.RepairDigest = result.repairTarget, result.repairDigest
	if runErr == nil {
		state.RepairFailures, state.RepairStopped = 0, false
		state.RepairPending = true
		return
	}
	state.RepairFailures++
	if state.RepairFailures <= len(repairRetryDelays) {
		state.RepairRetryAt = finished.Add(repairRetryDelays[state.RepairFailures-1])
	} else {
		state.RepairStopped = true
	}
}

func (c *Controller) repairEligibility(
	ctx context.Context,
	job registeredJob,
	snapshot Snapshot,
	state *JobState,
) (ready, blocked, changed bool, reason string, err error) {
	if job.jobType != semanticMemoryExtractionJobType ||
		(!state.RepairPending && state.RepairRetryAt.IsZero() && !state.RepairStopped) {
		return
	}
	var repairable bool
	for _, rule := range job.state {
		if rule.ID != memorySurfaceRepairableRuleID {
			continue
		}
		repairable, reason, err = rule.Evaluate(ctx, snapshot, *state)
		if err != nil {
			return
		}
		break
	}
	if !repairable {
		state.RepairPending, state.RepairStopped = false, false
		state.RepairRetryAt = time.Time{}
		state.RepairFailures = 0
		state.MaintenancePending = true
		changed = true
		return
	}
	if state.RepairTarget != "" && state.RepairDigest != "" {
		var raw []byte
		var existed bool
		raw, existed, err = c.loader.ReadFileBytes(ctx, state.RepairTarget)
		if err != nil {
			return
		}
		if repairInputDigest(raw, existed) != state.RepairDigest {
			state.RepairFailures, state.RepairStopped = 0, false
			state.RepairRetryAt = time.Time{}
			state.RepairPending = true
			changed = true
		}
	}
	if state.RepairStopped ||
		(!state.RepairRetryAt.IsZero() && snapshot.NowUTC.Before(state.RepairRetryAt)) {
		blocked = true
		return
	}
	ready = true
	return
}

func (c *Controller) queueRepair(ctx context.Context) error {
	for _, job := range c.jobs {
		if job.jobType != semanticMemoryExtractionJobType {
			continue
		}
		state, err := c.store.LoadJobState(ctx, job.jobType)
		if err != nil {
			return err
		}
		state.RepairPending = true
		if err := c.store.StoreJobState(ctx, job.jobType, state); err != nil {
			return err
		}
		c.NotifyStateChange()
	}
	return nil
}

func (c *Controller) notifyRepairProgress(ctx context.Context) error {
	for _, job := range c.jobs {
		state, err := c.store.LoadJobState(ctx, job.jobType)
		if err != nil {
			return err
		}
		if !state.WaitingForRepair {
			continue
		}
		state.MaintenancePending = true
		state.TranscriptPending = true
		if err := c.store.StoreJobState(ctx, job.jobType, state); err != nil {
			return err
		}
	}
	c.NotifyStateChange()
	return nil
}

func (c *Controller) bootstrapRepair(ctx context.Context) error {
	for _, job := range c.jobs {
		if job.jobType != semanticMemoryExtractionJobType {
			continue
		}
		state, err := c.store.LoadJobState(ctx, job.jobType)
		if err != nil {
			return err
		}
		for _, rule := range job.state {
			if rule.ID != memorySurfaceRepairableRuleID {
				continue
			}
			run, _, err := rule.Evaluate(
				ctx,
				Snapshot{NowUTC: time.Now().UTC(), Loader: c.loader},
				state,
			)
			if err != nil {
				return err
			}
			if run {
				return c.queueRepair(ctx)
			}
		}
	}
	return nil
}
