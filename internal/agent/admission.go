package agent

import (
	"context"
	"errors"
	"fmt"
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
}

type submission struct {
	prompt    string
	exclusive bool
	run       func(context.Context) (*fantasy.AgentResult, error)
}

type admission struct {
	mu       sync.Mutex
	owners   map[string]*submissionOwner
	queues   map[string][]submission
	lifetime context.Context
}

func newAdmission(ctx context.Context) *admission {
	return &admission{owners: make(map[string]*submissionOwner), queues: make(map[string][]submission), lifetime: ctx}
}

func (a *admission) claim(ctx context.Context, id string) *submissionOwner {
	ctx, cancel := context.WithCancel(ctx)
	owner := &submissionOwner{sessionID: id, cancel: cancel}
	owner.ctx = context.WithValue(ctx, ownerKey{}, owner)
	a.owners[id] = owner
	return owner
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
	if a.owners[id] != nil || len(a.queues[id]) > 0 {
		if job.exclusive {
			a.mu.Unlock()
			return nil, fmt.Errorf("admitting submission: %w", ErrSessionBusy)
		}
		a.queues[id] = append(a.queues[id], job)
		if a.owners[id] != nil {
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

func (a *admission) claimIdle(ctx context.Context, id string, check func() error) (*submissionOwner, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.owners[id] != nil {
		return nil, ErrSessionBusy
	}
	if err := check(); err != nil {
		return nil, err
	}
	return a.claim(ctx, id), nil
}

func (a *admission) execute(owner *submissionOwner, job submission) (result *fantasy.AgentResult, err error) {
	defer func() {
		a.mu.Lock()
		if a.owners[owner.sessionID] == owner {
			delete(a.owners, owner.sessionID)
		}
		a.mu.Unlock()
		owner.cancel()
	}()
	result, err = job.run(owner.ctx)
	a.mu.Lock()
	var next *submissionOwner
	var queued submission
	if a.owners[owner.sessionID] == owner {
		delete(a.owners, owner.sessionID)
		if (err == nil || owner.handoffOnError) && owner.ctx.Err() == nil && len(a.queues[owner.sessionID]) > 0 {
			queued = a.queues[owner.sessionID][0]
			a.queues[owner.sessionID] = a.queues[owner.sessionID][1:]
			next = a.claim(a.lifetime, owner.sessionID)
		}
	}
	a.mu.Unlock()
	owner.cancel()
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

func (a *admission) enqueue(id string, job submission) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.queues[id] = append(a.queues[id], job)
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
		prompts = append(prompts, job.prompt)
	}
	return prompts
}

func (a *admission) clear(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.queues, id)
}

func (a *admission) cancel(id string) {
	a.mu.Lock()
	owner := a.owners[id]
	delete(a.queues, id)
	a.mu.Unlock()
	if owner != nil {
		owner.cancel()
	}
}

func (a *admission) cancelAll() {
	a.mu.Lock()
	var owners []*submissionOwner
	for _, owner := range a.owners {
		owners = append(owners, owner)
	}
	clear(a.queues)
	a.mu.Unlock()
	for _, owner := range owners {
		owner.cancel()
	}
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
