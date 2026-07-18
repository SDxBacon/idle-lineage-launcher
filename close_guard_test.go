package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type closeGuardManagerStub struct {
	policy      updateClosePolicy
	state       GameState
	cancelErr   error
	cancelCalls int
	waitCalls   int
	onCancel    func()
	onWait      func()
}

func (manager *closeGuardManagerStub) UpdateClosePolicy() updateClosePolicy {
	return manager.policy
}

func (manager *closeGuardManagerStub) CancelUpdate() error {
	manager.cancelCalls++
	if manager.onCancel != nil {
		manager.onCancel()
	}
	return manager.cancelErr
}

func (manager *closeGuardManagerStub) WaitForIdle() {
	manager.waitCalls++
	if manager.onWait != nil {
		manager.onWait()
	}
}

func (manager *closeGuardManagerStub) State() GameState {
	return manager.state
}

func TestUpdateCloseCoordinatorHandlesClosePolicies(t *testing.T) {
	tests := []struct {
		name      string
		policy    updateClosePolicy
		wantAllow bool
		wantMode  CloseGuardMode
	}{
		{name: "allow", policy: updateCloseAllow, wantAllow: true},
		{name: "confirm cancellation", policy: updateCloseConfirmCancel, wantMode: closeGuardConfirmCancel},
		{name: "block critical work", policy: updateCloseBlock, wantMode: closeGuardBlocked},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := &closeGuardManagerStub{
				policy: test.policy,
				state:  GameState{ProgressPhase: "下載更新檔案"},
			}
			var events []CloseGuardEvent
			coordinator := newUpdateCloseCoordinator(manager, func(event CloseGuardEvent) {
				events = append(events, event)
			}, func() {})

			if allowed := coordinator.HandleCloseRequest(); allowed != test.wantAllow {
				t.Fatalf("unexpected close result: got %v, want %v", allowed, test.wantAllow)
			}
			if test.wantAllow {
				if len(events) != 0 {
					t.Fatalf("allowed close emitted a guard event: %+v", events)
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("expected one guard event, got %+v", events)
			}
			if events[0].Mode != test.wantMode || events[0].ProgressPhase != "下載更新檔案" {
				t.Fatalf("unexpected guard event: %+v", events[0])
			}
		})
	}
}

func TestCancelUpdateAndCloseCancelsWaitsAndBypassesGuardOnce(t *testing.T) {
	manager := &closeGuardManagerStub{policy: updateCloseConfirmCancel}
	manager.onCancel = func() {
		manager.policy = updateCloseAllow
	}
	var events []CloseGuardEvent
	var closeAllowed []bool
	var closeCalls int
	var coordinator *updateCloseCoordinator
	coordinator = newUpdateCloseCoordinator(manager, func(event CloseGuardEvent) {
		events = append(events, event)
	}, func() {
		closeCalls++
		closeAllowed = append(closeAllowed, coordinator.HandleCloseRequest())
	})

	if err := coordinator.CancelUpdateAndClose(); err != nil {
		t.Fatal(err)
	}
	if manager.cancelCalls != 1 || manager.waitCalls != 1 {
		t.Fatalf("unexpected cancel/wait calls: cancel=%d wait=%d", manager.cancelCalls, manager.waitCalls)
	}
	if closeCalls != 1 || len(closeAllowed) != 1 || !closeAllowed[0] {
		t.Fatalf("initiated close did not consume its bypass: calls=%d allowed=%v", closeCalls, closeAllowed)
	}

	manager.policy = updateCloseBlock
	manager.state.ProgressPhase = "套用遊戲版本"
	if coordinator.HandleCloseRequest() {
		t.Fatal("one-shot bypass allowed a second close request")
	}
	if len(events) != 1 || events[0].Mode != closeGuardBlocked {
		t.Fatalf("second close was not guarded: %+v", events)
	}

	if err := coordinator.CancelUpdateAndClose(); err != nil {
		t.Fatal(err)
	}
	if closeCalls != 1 {
		t.Fatalf("repeated completion initiated close %d times", closeCalls)
	}
}

func TestCancelUpdateAndCloseRechecksPhaseRace(t *testing.T) {
	manager := &closeGuardManagerStub{policy: updateCloseConfirmCancel}
	manager.onCancel = func() {
		manager.policy = updateCloseBlock
	}
	closeCalls := 0
	var events []CloseGuardEvent
	coordinator := newUpdateCloseCoordinator(manager, func(event CloseGuardEvent) {
		events = append(events, event)
	}, func() {
		closeCalls++
	})

	err := coordinator.CancelUpdateAndClose()
	if err == nil || !strings.Contains(err.Error(), "不可中斷") {
		t.Fatalf("unexpected race error: %v", err)
	}
	if manager.cancelCalls != 1 || manager.waitCalls != 1 || closeCalls != 0 {
		t.Fatalf("race was not safely blocked: cancel=%d wait=%d close=%d", manager.cancelCalls, manager.waitCalls, closeCalls)
	}
	if len(events) != 1 || events[0].Mode != closeGuardBlocked {
		t.Fatalf("phase race did not replace confirmation with blocked state: %+v", events)
	}
}

func TestCancelUpdateAndCloseRejectsCriticalPhaseBeforeCancellation(t *testing.T) {
	manager := &closeGuardManagerStub{policy: updateCloseBlock}
	closeCalls := 0
	coordinator := newUpdateCloseCoordinator(manager, nil, func() {
		closeCalls++
	})

	if err := coordinator.CancelUpdateAndClose(); err == nil {
		t.Fatal("expected critical update to reject close")
	}
	if manager.cancelCalls != 0 || manager.waitCalls != 0 || closeCalls != 0 {
		t.Fatalf("critical close performed work: cancel=%d wait=%d close=%d", manager.cancelCalls, manager.waitCalls, closeCalls)
	}
}

func TestCancelUpdateAndCloseReturnsCancellationError(t *testing.T) {
	want := errors.New("update became critical")
	manager := &closeGuardManagerStub{policy: updateCloseConfirmCancel, cancelErr: want}
	coordinator := newUpdateCloseCoordinator(manager, nil, func() {
		t.Fatal("close must not be initiated after a cancellation error")
	})

	if err := coordinator.CancelUpdateAndClose(); !errors.Is(err, want) {
		t.Fatalf("unexpected cancellation error: %v", err)
	}
	if manager.waitCalls != 0 {
		t.Fatal("coordinator waited after cancellation was rejected")
	}
}

func TestCancelUpdateAndCloseAllowsCompletedUpdate(t *testing.T) {
	manager := &closeGuardManagerStub{policy: updateCloseAllow}
	var coordinator *updateCloseCoordinator
	closeAllowed := false
	coordinator = newUpdateCloseCoordinator(manager, nil, func() {
		closeAllowed = coordinator.HandleCloseRequest()
	})

	if err := coordinator.CancelUpdateAndClose(); err != nil {
		t.Fatal(err)
	}
	if !closeAllowed {
		t.Fatal("completed update did not close")
	}
	if manager.cancelCalls != 0 || manager.waitCalls != 0 {
		t.Fatalf("completed update was cancelled or waited: cancel=%d wait=%d", manager.cancelCalls, manager.waitCalls)
	}
}

func TestCancelUpdateAndCloseCoalescesRepeatedRequests(t *testing.T) {
	waitStarted := make(chan struct{})
	releaseWait := make(chan struct{})
	manager := &closeGuardManagerStub{policy: updateCloseConfirmCancel}
	manager.onCancel = func() {
		manager.policy = updateCloseAllow
	}
	manager.onWait = func() {
		close(waitStarted)
		<-releaseWait
	}
	closeCalls := 0
	coordinator := newUpdateCloseCoordinator(manager, nil, func() {
		closeCalls++
	})

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- coordinator.CancelUpdateAndClose()
	}()
	<-waitStarted

	if err := coordinator.CancelUpdateAndClose(); err != nil {
		t.Fatal(err)
	}
	close(releaseWait)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if manager.cancelCalls != 1 || manager.waitCalls != 1 || closeCalls != 1 {
		t.Fatalf("repeated request was not coalesced: cancel=%d wait=%d close=%d", manager.cancelCalls, manager.waitCalls, closeCalls)
	}
}

func TestLauncherServiceCancelUpdateDelegatesToManager(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	manager := &gameManager{
		state:  GameState{Status: StatusUpdating, ProgressCancellable: true},
		cancel: cancel,
	}
	service := &LauncherService{manager: manager}

	if err := service.CancelUpdate(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("service did not cancel the active update")
	}
}

func TestLauncherServiceCancelUpdateAndCloseDelegatesToCoordinator(t *testing.T) {
	manager := &closeGuardManagerStub{policy: updateCloseAllow}
	closed := false
	service := &LauncherService{
		closeGuard: newUpdateCloseCoordinator(manager, nil, func() {
			closed = true
		}),
	}

	if err := service.CancelUpdateAndClose(); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("service did not initiate application close")
	}
}

func TestLauncherServiceUpdateCoordinationRejectsMissingDependencies(t *testing.T) {
	var service *LauncherService
	for name, call := range map[string]func() error{
		"cancel update":           service.CancelUpdate,
		"cancel update and close": service.CancelUpdateAndClose,
		"retry recovery":          service.RetryUpdateRecovery,
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("expected unavailable dependency error")
			}
		})
	}
}
