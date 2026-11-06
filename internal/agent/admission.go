package agent

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrAdmission = errors.New("agent: admission rejected")

type Admission struct {
	slots          chan struct{}
	perCallerLimit int
	mu             sync.Mutex
	perCaller      map[string]int
	capacityRPS    float64
	capacityBurst  float64
	capacityTokens float64
	capacityAt     time.Time
	now            func() time.Time
}

func NewAdmission(maxConcurrent, perCaller, capacityRPS int) *Admission {
	if maxConcurrent <= 0 {
		maxConcurrent = 64
	}
	if perCaller <= 0 {
		perCaller = 10
	}
	if capacityRPS <= 0 {
		capacityRPS = 10
	}
	return &Admission{
		slots:          make(chan struct{}, maxConcurrent),
		perCallerLimit: perCaller,
		perCaller:      make(map[string]int),
		capacityRPS:    float64(capacityRPS),
		capacityBurst:  1,
		capacityTokens: 1,
		capacityAt:     time.Now(),
		now:            time.Now,
	}
}

func (admission *Admission) AcquireOperation(ctx context.Context, caller string) (func(), error) {
	select {
	case admission.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, ErrAdmission
	}
	admission.mu.Lock()
	if admission.perCaller[caller] >= admission.perCallerLimit {
		admission.mu.Unlock()
		<-admission.slots
		return nil, ErrAdmission
	}
	admission.perCaller[caller]++
	admission.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			admission.mu.Lock()
			admission.perCaller[caller]--
			if admission.perCaller[caller] <= 0 {
				delete(admission.perCaller, caller)
			}
			admission.mu.Unlock()
			<-admission.slots
		})
	}, nil
}

func (admission *Admission) AcquireCapacity() error {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	now := admission.now()
	elapsed := now.Sub(admission.capacityAt).Seconds()
	if elapsed > 0 {
		admission.capacityTokens += elapsed * admission.capacityRPS
		if admission.capacityTokens > admission.capacityBurst {
			admission.capacityTokens = admission.capacityBurst
		}
		admission.capacityAt = now
	}
	if admission.capacityTokens < 1 {
		return ErrAdmission
	}
	admission.capacityTokens--
	return nil
}
