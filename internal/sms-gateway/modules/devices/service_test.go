package devices //nolint:testpackage // The removal race requires access to the private cache and repository seam.

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestServiceRemoveInvalidatesCacheAfterRepositoryRemoval(t *testing.T) {
	t.Parallel()

	device := Device{DeviceInput: DeviceInput{ID: "device-id", UserID: "user-id", AuthToken: "auth-token"}}
	deviceCache := newCache()
	if err := deviceCache.Set(device); err != nil {
		t.Fatalf("cache device: %v", err)
	}

	cachePresentDuringRemoval := false
	repository := &removeRepositoryStub{
		selectDevices: []Device{device},
		get: func(context.Context, ...SelectFilter) (*Device, error) {
			result := device
			return &result, nil
		},
		remove: func(context.Context, ...SelectFilter) error {
			cached, err := deviceCache.GetByToken(device.AuthToken)
			cachePresentDuringRemoval = err == nil && cached.ID == device.ID
			return nil
		},
	}
	service := &Service{
		devices: repository,
		cache:   deviceCache,
		logger:  zap.NewNop(),
	}

	if err := service.Remove(context.Background(), device.UserID); err != nil {
		t.Fatalf("remove device: %v", err)
	}
	if !cachePresentDuringRemoval {
		t.Fatal("device cache was invalidated before repository removal")
	}
	if repository.getCalls != 0 {
		t.Fatalf("repository get calls during removal = %d, want 0", repository.getCalls)
	}
	if repository.removeCalls != 1 {
		t.Fatalf("repository removal calls = %d, want 1", repository.removeCalls)
	}
	if _, err := deviceCache.GetByToken(device.AuthToken); err == nil {
		t.Fatal("device remained cached after successful repository removal")
	}
}

func TestServiceRemoveRejectsLookupThatReadDeviceBeforeDeletion(t *testing.T) {
	t.Parallel()

	device := Device{DeviceInput: DeviceInput{ID: "device-id", UserID: "user-id", AuthToken: "auth-token"}}
	getStarted := make(chan struct{})
	allowGet := make(chan struct{})
	t.Cleanup(func() { closeSignal(allowGet) })

	firstGet := true
	repository := &removeRepositoryStub{
		selectDevices: []Device{device},
		get: func(context.Context, ...SelectFilter) (*Device, error) {
			if firstGet {
				firstGet = false
				close(getStarted)
				<-allowGet

				result := device
				return &result, nil
			}

			return nil, ErrNotFound
		},
		remove: func(context.Context, ...SelectFilter) error {
			return nil
		},
	}
	deviceCache := newCache()
	service := &Service{
		devices: repository,
		cache:   deviceCache,
		logger:  zap.NewNop(),
	}

	type lookupResult struct {
		device *Device
		err    error
	}
	lookupResults := make(chan lookupResult, 1)
	go func() {
		result, err := service.GetByToken(context.Background(), device.AuthToken)
		lookupResults <- lookupResult{device: result, err: err}
	}()

	waitForTestResult(t, getStarted, "initial token lookup to reach the repository")

	removeResults := make(chan error, 1)
	go func() {
		removeResults <- service.Remove(context.Background(), device.UserID)
	}()
	if err := waitForTestResult(t, removeResults, "device removal to finish"); err != nil {
		t.Fatalf("remove device: %v", err)
	}

	closeSignal(allowGet)
	lookup := waitForTestResult(t, lookupResults, "in-flight token lookup to finish")
	if !errors.Is(lookup.err, ErrNotFound) {
		t.Fatalf("lookup error = %v, want %v", lookup.err, ErrNotFound)
	}
	if lookup.device != nil {
		t.Fatalf("lookup returned deleted device %q", lookup.device.ID)
	}
	if repository.getCalls != 2 {
		t.Fatalf("repository get calls = %d, want 2", repository.getCalls)
	}
	if _, err := deviceCache.GetByToken(device.AuthToken); err == nil {
		t.Fatal("in-flight lookup restored the deleted credential")
	}
}

func TestServiceRemovePreservesCacheWhenRepositoryRemovalFails(t *testing.T) {
	t.Parallel()

	removeErr := errors.New("remove failed")
	device := Device{DeviceInput: DeviceInput{ID: "device-id", UserID: "user-id", AuthToken: "auth-token"}}
	deviceCache := newCache()
	if err := deviceCache.Set(device); err != nil {
		t.Fatalf("cache device: %v", err)
	}

	service := &Service{
		devices: &removeRepositoryStub{
			selectDevices: []Device{device},
			remove: func(context.Context, ...SelectFilter) error {
				return removeErr
			},
		},
		cache:  deviceCache,
		logger: zap.NewNop(),
	}

	err := service.Remove(context.Background(), device.UserID)
	if !errors.Is(err, removeErr) {
		t.Fatalf("remove error = %v, want %v", err, removeErr)
	}
	if cached, cacheErr := deviceCache.GetByToken(device.AuthToken); cacheErr != nil {
		t.Fatalf("device was evicted after failed repository removal: %v", cacheErr)
	} else if cached.ID != device.ID {
		t.Fatalf("cached device ID = %q, want %q", cached.ID, device.ID)
	}
}

type removeRepositoryStub struct {
	*Repository

	selectDevices []Device
	selectErr     error
	get           func(context.Context, ...SelectFilter) (*Device, error)
	remove        func(context.Context, ...SelectFilter) error
	getCalls      int
	removeCalls   int
}

func (r *removeRepositoryStub) Select(context.Context, ...SelectFilter) ([]Device, error) {
	return r.selectDevices, r.selectErr
}

func (r *removeRepositoryStub) Get(ctx context.Context, filter ...SelectFilter) (*Device, error) {
	r.getCalls++
	return r.get(ctx, filter...)
}

func (r *removeRepositoryStub) Remove(ctx context.Context, filter ...SelectFilter) error {
	r.removeCalls++
	return r.remove(ctx, filter...)
}

func waitForTestResult[T any](t *testing.T, results <-chan T, operation string) T {
	t.Helper()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	select {
	case result := <-results:
		return result
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", operation)
		var zero T
		return zero
	}
}

func closeSignal(signal chan struct{}) {
	select {
	case <-signal:
	default:
		close(signal)
	}
}
