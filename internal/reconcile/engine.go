package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const rollbackTimeout = 30 * time.Second

// Executor applies, verifies, and rolls back service operations.
type Executor interface {
	Apply(context.Context, Operation) error
	Verify(context.Context, Operation) error
	Rollback(context.Context, Operation) error
}

// Engine executes reconciliation plans.
type Engine struct {
	executor Executor
}

// NewEngine returns an Engine backed by executor.
func NewEngine(executor Executor) (*Engine, error) {
	if executor == nil {
		return nil, errors.New("reconciliation executor is required")
	}
	return &Engine{executor: executor}, nil
}

// Execute applies and verifies a plan. Dry-run execution performs no mutations.
func (e *Engine) Execute(ctx context.Context, plan Plan, dryRun bool) error {
	if dryRun || len(plan.Operations) == 0 {
		return nil
	}

	applied := make([]Operation, 0, len(plan.Operations))
	for _, operation := range plan.Operations {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, e.rollback(ctx, applied))
		}
		if err := e.executor.Apply(ctx, operation); err != nil {
			return errors.Join(
				fmt.Errorf("apply %s for service %q: %w", operation.Kind, operation.ServiceID(), err),
				e.rollback(ctx, append(applied, operation)),
			)
		}
		applied = append(applied, operation)
		if err := e.executor.Verify(ctx, operation); err != nil {
			return errors.Join(
				fmt.Errorf("verify %s for service %q: %w", operation.Kind, operation.ServiceID(), err),
				e.rollback(ctx, applied),
			)
		}
	}

	return nil
}

func (e *Engine) rollback(ctx context.Context, applied []Operation) error {
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()

	var rollbackErrors []error
	for i := len(applied) - 1; i >= 0; i-- {
		operation := applied[i]
		if err := e.executor.Rollback(rollbackContext, operation); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback %s for service %q: %w", operation.Kind, operation.ServiceID(), err))
		}
	}
	return errors.Join(rollbackErrors...)
}
