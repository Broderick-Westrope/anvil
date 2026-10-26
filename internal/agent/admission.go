package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"charm.land/fantasy"
)

type ownerKey struct{}

type submissionOwner struct {
	onFinish       func()
	onIdle         func()
	handoffOnError bool
	sessionID      string
	ctx            context.Context
	cancel         context.CancelFunc
	// parent is the context the owner was claimed with; a continuation of
	// an awaited submission runs under it too.
	parent context.Context
	// done receives the result of an awaited submission. A continuation
	// takes it over, so the submitter gets the result of the finished work.
	done chan<- submissionResult
	// continued is set when the owner's work queued a continuation, which
	// runs next even while the session is held: it's the same logical
	// work, so a hold waits for it rather than cutting it off.
	continued bool
	// holdingPrompts is set on silent work run while the session's queued
	// prompts are held back after a failed turn; they stay held after it.
	holdingPrompts bool
}

type submission struct {
	prompt    string
	exclusive bool
	run       func(context.Context) (*fantasy.AgentResult, error)
	// done is set for awaited submissions, which receive their own result
	// here rather than whatever ran last in the session.
	done chan<- submissionResult
	// ctx is the submitter's context for an awaited submission, so the
	// run can be cancelled by its submitter even after it was queued.
	ctx context.Context
	// continuation marks work queued by continueWith.
	continuation bool
	// silent marks work that isn't a prompt, which queued() leaves out.
	silent bool
}

type submissionResult struct {
	result *fantasy.AgentResult
	err    error
}

type admission struct {
	mu     sync.Mutex
	owners map[string]*submissionOwner
	queues map[string][]submission
	// held counts the active holds on each session. While a session is
	// held, no new run starts in it; submissions queue instead.
	held map[string]int
	// ownerGone is closed, and reset, whenever an owner releases its
	// session, waking holds waiting for in-flight runs to finish.
	ownerGone chan struct{}
	lifetime  context.Context
	// gate admits a run started on its own goroutine (an awaited
	// submission on an idle session, or queued work after a hold). The
	// coordinator uses it so those runs count as active for Pause. It
	// must return once ctx ends.
	gate func(ctx context.Context) (release func(), err error)
}

func newAdmission(ctx context.Context) *admission {
	return &admission{
		owners:   make(map[string]*submissionOwner),
		queues:   make(map[string][]submission),
		held:     make(map[string]int),
		lifetime: ctx,
		gate:     func(context.Context) (func(), error) { return func() {}, nil },
	}
}

func (a *admission) claim(ctx context.Context, id string) *submissionOwner {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	owner := &submissionOwner{sessionID: id, cancel: cancel, parent: parent}
	owner.ctx = context.WithValue(ctx, ownerKey{}, owner)
	a.owners[id] = owner
	return owner
}

// claimFor claims the session for a queued job, under the job's own
// context when it has one.
func (a *admission) claimFor(job submission, id string) *submissionOwner {
	ctx := a.lifetime
	if job.ctx != nil {
		ctx = job.ctx
	}
	return a.claim(ctx, id)
}

// submitAwait submits job and waits for its own result. If the session is
// busy the job queues behind earlier work; work queued after it runs once
// it finishes, without delaying the result. Unlike submit, it never
// returns early for a queued job.
func (a *admission) submitAwait(ctx context.Context, id string, job submission) (*fantasy.AgentResult, error) {
	a.mu.Lock()
	if owner, ok := ctx.Value(ownerKey{}).(*submissionOwner); ok {
		// Already running as this session's owner, e.g. the coordinator's
		// run calling the session agent: run in place, as submit does.
		valid := owner.sessionID == id && a.owners[id] == owner
		a.mu.Unlock()
		if !valid {
			return nil, fmt.Errorf("invalid submission owner: %w", ErrSessionBusy)
		}
		return job.run(ctx)
	}
	a.mu.Unlock()
	done := make(chan submissionResult, 1)
	job.done, job.ctx = done, ctx
	a.mu.Lock()
	if err := ctx.Err(); err != nil {
		a.mu.Unlock()
		return nil, err
	}
	// The job joins the queue and, on an idle session, starts from its head,
	// which is the job itself unless earlier work is still waiting. Prompts
	// waiting on an idle session were held back by a failed turn; silent
	// work runs past them and leaves them held, as it does behind a turn.
	// (A script's turn is never left waiting on an idle session: it's
	// always handed the session.)
	a.queues[id] = append(a.queues[id], job)
	if a.owners[id] == nil && a.held[id] == 0 {
		q := a.queues[id]
		i, holding := 0, false
		if job.silent {
			i = slices.IndexFunc(q, func(j submission) bool { return j.silent })
			holding = slices.ContainsFunc(q, func(j submission) bool { return !j.silent })
		}
		job = q[i]
		a.queues[id] = append(q[:i:i], q[i+1:]...)
		owner := a.claimFor(job, id)
		owner.holdingPrompts = holding
		a.mu.Unlock()
		a.start(owner, job)
	} else {
		a.mu.Unlock()
	}
	select {
	case res := <-done:
		return res.result, res.err
	case <-ctx.Done():
		if a.withdraw(id, done) {
			return nil, ctx.Err()
		}
		// Already running: its context is ours, so it ends promptly.
		res := <-done
		return res.result, res.err
	}
}

// start runs an already claimed job on its own goroutine once the gate
// admits it. If the owner's context ends first, the job completes with
// that error through execute, so its submitter hears back, the session is
// released and the queue moves on as after any other failed run.
func (a *admission) start(owner *submissionOwner, job submission) {
	go func() {
		release, err := a.gate(owner.ctx)
		if err != nil {
			job.run = func(context.Context) (*fantasy.AgentResult, error) { return nil, err }
			_, _ = a.execute(owner, job)
			return
		}
		defer release()
		_, _ = a.execute(owner, job)
	}()
}

// withdraw removes a still-queued awaited submission.
func (a *admission) withdraw(id string, done chan<- submissionResult) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, job := range a.queues[id] {
		if job.done == done {
			a.queues[id] = append(a.queues[id][:i:i], a.queues[id][i+1:]...)
			return true
		}
	}
	return false
}

// continueWith queues job to continue the owner's work after it finishes,
// such as resuming after auto-compaction. If the owner's submission is
// awaited, the continuation runs next and its submitter gets the
// continuation's result instead.
func (a *admission) continueWith(ctx context.Context, id string, job submission) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil {
		// The work was cancelled (Esc, abort) before it could register its
		// continuation; cancelling means stop, so it doesn't continue.
		return
	}
	job.continuation = true
	owner, ok := ctx.Value(ownerKey{}).(*submissionOwner)
	if ok && owner != nil && owner.sessionID == id {
		owner.continued = true
	}
	if !ok || owner == nil || owner.sessionID != id || owner.done == nil {
		a.queues[id] = append(a.queues[id], job)
		return
	}
	job.done, job.ctx = owner.done, owner.parent
	owner.done = nil
	a.queues[id] = append([]submission{job}, a.queues[id]...)
}

// ownsWithNothingQueued reports whether ctx's run owns the session, isn't
// an awaited submission, and has nothing queued behind it. Work it then
// runs in place under ctx is part of the same run: cancelling it cancels
// that work too, and holds wait for it.
func (a *admission) ownsWithNothingQueued(ctx context.Context, id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	owner, ok := ctx.Value(ownerKey{}).(*submissionOwner)
	return ok && owner != nil && a.owners[id] == owner && owner.done == nil &&
		len(a.queues[id]) == 0 && ctx.Err() == nil
}

func (a *admission) submit(ctx context.Context, id string, job submission) (*fantasy.AgentResult, error) {
	a.mu.Lock()
	if owner, ok := ctx.Value(ownerKey{}).(*submissionOwner); ok {
		valid := owner.sessionID == id && a.owners[id] == owner
		a.mu.Unlock()
		if !valid {
			return nil, fmt.Errorf("invalid submission owner: %w", ErrSessionBusy)
		}
		return job.run(ctx)
	}
	if err := ctx.Err(); err != nil {
		a.mu.Unlock()
		return nil, err
	}
	if a.owners[id] != nil || len(a.queues[id]) > 0 || a.held[id] > 0 {
		if job.exclusive {
			a.mu.Unlock()
			return nil, fmt.Errorf("admitting submission: %w", ErrSessionBusy)
		}
		a.queues[id] = append(a.queues[id], job)
		if a.owners[id] != nil || a.held[id] > 0 {
			a.mu.Unlock()
			return nil, nil
		}
		job = a.queues[id][0]
		a.queues[id] = a.queues[id][1:]
	}
	owner := a.claim(ctx, id)
	a.mu.Unlock()
	return a.execute(owner, job)
}

// reserve stops new runs from starting in each of ids at once, without
// waiting for runs in flight. The returned release lifts it and starts
// work that queued behind it.
func (a *admission) reserve(ids []string) (release func()) {
	a.mu.Lock()
	for _, id := range ids {
		a.held[id]++
	}
	a.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { a.unhold(ids) }) }
}

// hold stops new runs from starting in each of ids, then waits until none
// of them has a run in flight. Holds nest. The returned release lifts the
// hold and starts work that queued behind it.
func (a *admission) hold(ctx context.Context, ids []string) (release func(), err error) {
	release = a.reserve(ids)
	for {
		a.mu.Lock()
		busy := slices.ContainsFunc(ids, func(id string) bool { return a.owners[id] != nil })
		if !busy {
			a.mu.Unlock()
			return release, nil
		}
		if a.ownerGone == nil {
			a.ownerGone = make(chan struct{})
		}
		gone := a.ownerGone
		a.mu.Unlock()
		select {
		case <-gone:
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
}

// releaseOwner removes the session's owner and wakes waiting holds. The
// caller must hold a.mu.
func (a *admission) releaseOwner(id string) {
	delete(a.owners, id)
	if a.ownerGone != nil {
		close(a.ownerGone)
		a.ownerGone = nil
	}
}

func (a *admission) unhold(ids []string) {
	type start struct {
		owner *submissionOwner
		job   submission
	}
	var starts []start
	a.mu.Lock()
	for _, id := range ids {
		if a.held[id]--; a.held[id] > 0 {
			continue
		}
		delete(a.held, id)
		if a.owners[id] != nil || len(a.queues[id]) == 0 {
			continue
		}
		job := a.queues[id][0]
		a.queues[id] = a.queues[id][1:]
		starts = append(starts, start{owner: a.claimFor(job, id), job: job})
	}
	a.mu.Unlock()
	for _, s := range starts {
		a.start(s.owner, s.job)
	}
}

func (a *admission) claimIdle(ctx context.Context, id string, check func() error) (*submissionOwner, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.owners[id] != nil || a.held[id] > 0 {
		return nil, ErrSessionBusy
	}
	if err := check(); err != nil {
		return nil, err
	}
	return a.claim(ctx, id), nil
}

func (a *admission) execute(owner *submissionOwner, job submission) (result *fantasy.AgentResult, err error) {
	a.mu.Lock()
	owner.done = job.done
	scriptTurn, silent := isScriptTurn(job), job.silent
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.owners[owner.sessionID] == owner {
			a.releaseOwner(owner.sessionID)
		}
		a.mu.Unlock()
		owner.cancel()
	}()
	result, err = job.run(owner.ctx)
	a.mu.Lock()
	var next *submissionOwner
	var queued submission
	if a.owners[owner.sessionID] == owner {
		a.releaseOwner(owner.sessionID)
		q := a.queues[owner.sessionID]
		i := slices.IndexFunc(q, func(job submission) bool { return job.continuation })
		if owner.continued && i >= 0 && a.held[owner.sessionID] > 0 {
			// A hold is waiting for this work to finish, which includes
			// its continuation; new submissions stay queued.
			queued = q[i]
			a.queues[owner.sessionID] = append(q[:i:i], q[i+1:]...)
			next = a.claimFor(queued, owner.sessionID)
		} else if len(q) > 0 && a.held[owner.sessionID] == 0 {
			if i, holding := a.nextInQueue(owner, scriptTurn, silent, err, q); i >= 0 {
				queued = q[i]
				a.queues[owner.sessionID] = append(q[:i:i], q[i+1:]...)
				next = a.claimFor(queued, owner.sessionID)
				next.holdingPrompts = holding
			}
		}
	}
	done := owner.done
	owner.done = nil
	a.mu.Unlock()
	owner.cancel()
	if done != nil {
		done <- submissionResult{result: result, err: err}
	}
	if err == nil && owner.onFinish != nil {
		owner.onFinish()
	}
	if next == nil {
		if err == nil && owner.onIdle != nil {
			owner.onIdle()
		}
		return result, err
	}
	if err == nil {
		return a.execute(next, queued)
	}
	if _, nextErr := a.execute(next, queued); nextErr != nil {
		err = errors.Join(err, nextErr)
	}
	return nil, err
}

// nextInQueue picks the index of the job that runs after owner's, or -1,
// and whether the queue's prompts are being held back. A failed or
// cancelled user turn normally holds back the queue, but never while a
// script's turn is queued anywhere in it: a workflow can't send a
// follow-up message to unblock itself, so the work queued ahead of its
// turn runs and then its turn does. While prompts are held, silent work (a
// marker, a metadata entry, a fork's copy) still runs, and the prompts stay
// held after it. Silent work's own outcome never holds them: the user
// can't see it, so its failure says nothing about their prompts.
func (a *admission) nextInQueue(owner *submissionOwner, scriptTurn, silent bool, err error, queue []submission) (int, bool) {
	if a.lifetime.Err() != nil {
		return -1, false
	}
	if scriptTurn || slices.ContainsFunc(queue, isScriptTurn) {
		return 0, false
	}
	held := owner.holdingPrompts
	if !silent {
		held = held || (err != nil && !owner.handoffOnError) || owner.ctx.Err() != nil
	}
	if !held {
		return 0, false
	}
	return slices.IndexFunc(queue, func(job submission) bool { return job.silent }), true
}

// isScriptTurn reports whether a job is a workflow script's awaited turn,
// as opposed to the user's prompt or silent work.
func isScriptTurn(job submission) bool {
	return job.done != nil && !job.silent
}

func (a *admission) busy(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id == "" {
		return len(a.owners) > 0
	}
	return a.owners[id] != nil
}

func (a *admission) queued(id string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var prompts []string
	for _, job := range a.queues[id] {
		// The user's prompts: what the queue pill lists and Esc clears.
		if !job.silent && !isScriptTurn(job) {
			prompts = append(prompts, job.prompt)
		}
	}
	return prompts
}

func (a *admission) clear(id string) {
	a.mu.Lock()
	dropped := a.dropPrompts(id)
	a.mu.Unlock()
	cancelAwaited(dropped)
}

// dropPrompts removes the queued prompts of a session and returns them:
// the user's, which Esc clears. Silent work (a workflow's marker, a
// metadata entry, a fork's copy) and a workflow script's queued turns
// aren't the user's to clear, so they stay queued and run once the session
// is free; a script is stopped by aborting its run. Callers hold a.mu.
func (a *admission) dropPrompts(id string) []submission {
	var kept, dropped []submission
	for _, job := range a.queues[id] {
		if job.silent || isScriptTurn(job) {
			kept = append(kept, job)
		} else {
			dropped = append(dropped, job)
		}
	}
	if len(kept) == 0 {
		delete(a.queues, id)
	} else {
		a.queues[id] = kept
	}
	return dropped
}

func (a *admission) cancel(id string) {
	a.mu.Lock()
	owner := a.owners[id]
	dropped := a.dropPrompts(id)
	// Cancelled under the lock, so continueWith sees either the queue
	// before it was cleared or the cancelled context, never neither.
	if owner != nil {
		owner.cancel()
	}
	a.mu.Unlock()
	cancelAwaited(dropped)
}

// cancelAwaited tells the submitters of dropped awaited jobs that their
// work was cancelled.
func cancelAwaited(dropped []submission) {
	for _, job := range dropped {
		if job.done != nil {
			job.done <- submissionResult{err: context.Canceled}
		}
	}
}

func (a *admission) cancelAll() {
	a.mu.Lock()
	var owners []*submissionOwner
	for _, owner := range a.owners {
		owners = append(owners, owner)
	}
	var dropped []submission
	for _, q := range a.queues {
		dropped = append(dropped, q...)
	}
	clear(a.queues)
	for _, owner := range owners {
		owner.cancel()
	}
	a.mu.Unlock()
	cancelAwaited(dropped)
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for a.busy("") {
		select {
		case <-timeout.C:
			return
		case <-tick.C:
		}
	}
}
