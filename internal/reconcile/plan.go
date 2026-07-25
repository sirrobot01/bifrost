package reconcile

import (
	"cmp"
	"fmt"
	"slices"
)

// OperationKind describes a service-level reconciliation change.
type OperationKind string

const (
	OperationCreate OperationKind = "create"
	OperationUpdate OperationKind = "update"
	OperationRemove OperationKind = "remove"
)

// Operation is one deterministic service-level reconciliation change.
type Operation struct {
	Kind    OperationKind
	Current Service
	Desired Service
}

// ServiceID returns the affected service ID.
func (o Operation) ServiceID() string {
	if o.Desired.ID != "" {
		return o.Desired.ID
	}
	return o.Current.ID
}

// Plan is an ordered set of reconciliation operations.
type Plan struct {
	Operations []Operation
}

// BuildPlan compares desired services with the currently applied services.
func BuildPlan(desired, current []Service) (Plan, error) {
	desiredByID, err := indexServices(desired)
	if err != nil {
		return Plan{}, fmt.Errorf("desired services: %w", err)
	}
	currentByID, err := indexServices(current)
	if err != nil {
		return Plan{}, fmt.Errorf("current services: %w", err)
	}

	operations := make([]Operation, 0, len(desired)+len(current))
	for id, service := range desiredByID {
		applied, exists := currentByID[id]
		switch {
		case !exists:
			operations = append(operations, Operation{Kind: OperationCreate, Desired: service})
		case applied != service:
			operations = append(operations, Operation{Kind: OperationUpdate, Current: applied, Desired: service})
		}
	}
	for id, service := range currentByID {
		if _, exists := desiredByID[id]; !exists {
			operations = append(operations, Operation{Kind: OperationRemove, Current: service})
		}
	}

	slices.SortFunc(operations, func(a, b Operation) int {
		if order := operationOrder(a.Kind) - operationOrder(b.Kind); order != 0 {
			return order
		}
		return cmp.Compare(a.ServiceID(), b.ServiceID())
	})

	return Plan{Operations: operations}, nil
}

func indexServices(services []Service) (map[string]Service, error) {
	indexed := make(map[string]Service, len(services))
	for _, service := range services {
		if err := service.Validate(); err != nil {
			return nil, fmt.Errorf("service %q: %w", service.ID, err)
		}
		if _, exists := indexed[service.ID]; exists {
			return nil, fmt.Errorf("duplicate service ID %q", service.ID)
		}
		indexed[service.ID] = service
	}
	return indexed, nil
}

func operationOrder(kind OperationKind) int {
	switch kind {
	case OperationCreate:
		return 0
	case OperationUpdate:
		return 1
	case OperationRemove:
		return 2
	default:
		return 3
	}
}
