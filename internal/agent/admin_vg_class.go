package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/voidstarit/kouja/internal/audit"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/warnings"
)

// VGRegistration is the stored registration plus one observation.
type VGRegistration struct {
	Name             string
	RegisteredVGUUID string
	Capacity         lvm.Capacity
	UUIDDrift        bool
	StorageClass     string
	ObservedAt       time.Time
}

// VGClassManager owns the shared VG/class policy and the vgClassGate.
type VGClassManager struct {
	vgs     VGService
	classes ClassRegistry
	gate    *sync.Mutex
	audit   *audit.Recorder
}

func NewVGClassManager(vgs VGService, classes ClassRegistry, gate *sync.Mutex, recorder *audit.Recorder) *VGClassManager {
	if gate == nil {
		gate = &sync.Mutex{}
	}
	return &VGClassManager{vgs: vgs, classes: classes, gate: gate, audit: recorder}
}

func (manager *VGClassManager) RegisterVG(ctx context.Context, name, expectedUUID string, replace bool, event state.AuditEvent) (VGRegistration, []warnings.Warning, error) {
	manager.gate.Lock()
	defer manager.gate.Unlock()
	event.Action = "volume_group_register"
	event.Result = "succeeded"
	event.ResourceType = "volume_group"
	event.ResourceID = name
	event.Name = name
	event.Detail = "replace=false"
	capacity, err := manager.vgs.RegisterWithAudit(ctx, name, expectedUUID, replace, &event)
	if err != nil {
		return VGRegistration{}, nil, err
	}
	registration, err := manager.registration(ctx, name)
	if err != nil {
		return VGRegistration{}, nil, err
	}
	if registration.Capacity.VGUUID == "" {
		registration.Capacity = capacity
	}
	return registration, nil, nil
}

func (manager *VGClassManager) DeregisterVG(ctx context.Context, name string, event state.AuditEvent) ([]warnings.Warning, error) {
	manager.gate.Lock()
	defer manager.gate.Unlock()
	event.Action = "volume_group_deregister"
	event.Result = "succeeded"
	event.ResourceType = "volume_group"
	event.ResourceID = name
	event.Name = name
	if err := manager.vgs.DeregisterWithAudit(ctx, name, &event); err != nil {
		return nil, err
	}
	return nil, nil
}

func (manager *VGClassManager) GetVG(ctx context.Context, name string) (VGRegistration, error) {
	return manager.registration(ctx, name)
}

func (manager *VGClassManager) SetClass(ctx context.Context, class, vg string, event state.AuditEvent) ([]warnings.Warning, error) {
	manager.gate.Lock()
	defer manager.gate.Unlock()
	if _, err := manager.vgs.Get(ctx, vg); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVGNotFound, err)
	}
	values, err := manager.classes.Set(ctx, class, vg)
	if err != nil {
		return nil, err
	}
	result := warningList(values)
	event.Action = "storage_class_set"
	event.Result = "succeeded"
	event.ResourceType = "storage_class"
	event.ResourceID = class
	event.Name = class
	event.Detail = vg
	if manager.audit != nil {
		if err := manager.audit.Record(ctx, audit.Event{
			Caller: event.CallerOwner, OperationID: event.OperationID, RequestID: event.RequestID,
			ResourceType: event.ResourceType, ResourceID: event.ResourceID, Name: event.Name,
			Action: event.Action, Result: event.Result, Detail: event.Detail, RemoteAddr: event.RemoteAddr,
		}); err != nil {
			result = append(result, warnings.Warning{Code: warnings.CodeAuditWriteFailed, Message: err.Error()})
		}
	}
	return result, nil
}

func (manager *VGClassManager) RemoveClass(ctx context.Context, class string, event state.AuditEvent) ([]warnings.Warning, error) {
	manager.gate.Lock()
	defer manager.gate.Unlock()
	values, err := manager.classes.Remove(ctx, class)
	if err != nil {
		return nil, err
	}
	result := warningList(values)
	event.Action = "storage_class_remove"
	event.Result = "succeeded"
	event.ResourceType = "storage_class"
	event.ResourceID = class
	event.Name = class
	if manager.audit != nil {
		if err := manager.audit.Record(ctx, audit.Event{
			Caller: event.CallerOwner, OperationID: event.OperationID, RequestID: event.RequestID,
			ResourceType: event.ResourceType, ResourceID: event.ResourceID, Name: event.Name,
			Action: event.Action, Result: event.Result, RemoteAddr: event.RemoteAddr,
		}); err != nil {
			result = append(result, warnings.Warning{Code: warnings.CodeAuditWriteFailed, Message: err.Error()})
		}
	}
	return result, nil
}

func (manager *VGClassManager) ListClasses(context.Context) (map[string]string, error) {
	if manager.classes == nil {
		return map[string]string{}, nil
	}
	return manager.classes.Snapshot(), nil
}

func (manager *VGClassManager) GetClass(_ context.Context, class string) (string, error) {
	if manager.classes == nil {
		return "", fmt.Errorf("agent: class registry is not configured")
	}
	classes := manager.classes.Snapshot()
	vg, ok := classes[class]
	if !ok {
		return "", fmt.Errorf("agent: class not found")
	}
	return vg, nil
}

func (manager *VGClassManager) registration(ctx context.Context, name string) (VGRegistration, error) {
	vg, err := manager.vgs.Get(ctx, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return VGRegistration{}, ErrVGNotFound
		}
		return VGRegistration{}, err
	}
	registration := VGRegistration{Name: vg.Name, RegisteredVGUUID: vg.UUID}
	if manager.classes != nil {
		registration.StorageClass = manager.classes.ClassName(vg.Name)
	}
	capacity, err := manager.vgs.Inspect(ctx, name)
	if err != nil {
		registration.Capacity = lvm.Capacity{VGName: name, VGState: "unavailable"}
		return registration, nil
	}
	registration.Capacity = capacity
	registration.ObservedAt = capacity.ObservedAt
	registration.UUIDDrift = capacity.VGUUID != "" && capacity.VGUUID != vg.UUID
	return registration, nil
}

func warningList(values []string) []warnings.Warning {
	result := make([]warnings.Warning, 0, len(values))
	for _, value := range values {
		result = append(result, warnings.Warning{Code: value, Message: value})
	}
	return result
}
