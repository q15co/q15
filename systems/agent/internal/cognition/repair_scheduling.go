package cognition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
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
	state.RepairStops = stoppedRepairDigests(*state)
	state.RepairPending = false
	state.RepairRetryAt = time.Time{}
	if !result.Repair {
		state.RepairFailures, state.RepairStopped = 0, false
		return
	}
	if state.RepairTarget != result.repairTarget || state.RepairDigest != result.repairDigest {
		state.RepairFailures, state.RepairStopped = 0, false
	}
	state.RepairTarget, state.RepairDigest = result.repairTarget, result.repairDigest
	if runErr == nil {
		delete(state.RepairStops, result.repairTarget)
		state.RepairFailures, state.RepairStopped = 0, false
		state.RepairPending = true
		return
	}
	state.RepairFailures++
	if state.RepairFailures <= len(repairRetryDelays) {
		state.RepairRetryAt = finished.Add(repairRetryDelays[state.RepairFailures-1])
	} else {
		state.RepairStopped = true
		state.RepairStops[state.RepairTarget] = state.RepairDigest
	}
}

func stoppedRepairDigests(state JobState) map[string]string {
	stops := maps.Clone(state.RepairStops)
	if stops == nil {
		stops = make(map[string]string)
	}
	if state.RepairStopped && state.RepairTarget != "" && state.RepairDigest != "" {
		stops[state.RepairTarget] = state.RepairDigest
	}
	return stops
}

func withoutStoppedRepairTargets(
	ctx context.Context,
	loader ContextLoader,
	targets []MemorySurfaceTarget,
	state JobState,
) ([]MemorySurfaceTarget, error) {
	stops := stoppedRepairDigests(state)
	if len(stops) == 0 {
		return targets, nil
	}
	available := make([]MemorySurfaceTarget, 0, len(targets))
	for _, target := range targets {
		if digest, stopped := stops[target.Path]; stopped {
			raw, existed, err := loader.ReadFileBytes(ctx, target.Path)
			if err != nil {
				return nil, fmt.Errorf("read stopped repair target %s: %w", target.Path, err)
			}
			if repairInputDigest(raw, existed) == digest {
				continue
			}
		}
		available = append(available, target)
	}
	return available, nil
}

func (c *Controller) repairEligibility(
	ctx context.Context,
	job registeredJob,
	snapshot Snapshot,
	state *JobState,
) (ready, blocked, changed bool, reason string, err error) {
	if job.jobType != semanticMemoryExtractionJobType ||
		(!state.RepairPending && state.RepairRetryAt.IsZero() && !state.RepairStopped && len(state.RepairStops) == 0) {
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
			delete(state.RepairStops, state.RepairTarget)
			state.RepairFailures, state.RepairStopped = 0, false
			state.RepairRetryAt = time.Time{}
			state.RepairPending = true
			changed = true
		}
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
		unstopped := *state
		unstopped.RepairStopped, unstopped.RepairStops = false, nil
		for _, rule := range job.state {
			if rule.ID != memorySurfaceRepairableRuleID {
				continue
			}
			repairable, _, err = rule.Evaluate(ctx, snapshot, unstopped)
			if err != nil {
				return
			}
			break
		}
		if repairable {
			blocked = true
			return
		}
		state.RepairPending, state.RepairStopped = false, false
		state.RepairStops = nil
		state.RepairRetryAt = time.Time{}
		state.RepairFailures = 0
		state.MaintenancePending = true
		changed = true
		return
	}
	if !state.RepairRetryAt.IsZero() && snapshot.NowUTC.Before(state.RepairRetryAt) {
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
