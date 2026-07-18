package main

import (
	"errors"
	"sync"
)

const closeGuardEventName = "launcher:close-guard"

type CloseGuardMode string

const (
	closeGuardConfirmCancel CloseGuardMode = "confirm_cancel"
	closeGuardBlocked       CloseGuardMode = "blocked"
)

// CloseGuardEvent tells the frontend why a close request was intercepted.
type CloseGuardEvent struct {
	Mode          CloseGuardMode `json:"mode"`
	ProgressPhase string         `json:"progressPhase"`
}

type updateCloseManager interface {
	UpdateClosePolicy() updateClosePolicy
	CancelUpdate() error
	WaitForIdle()
	State() GameState
}

// updateCloseCoordinator keeps Wails lifecycle hooks independent from the
// update implementation and makes close/cancel races testable without a UI.
type updateCloseCoordinator struct {
	mu sync.Mutex

	manager updateCloseManager
	emit    func(CloseGuardEvent)
	close   func()

	bypassOnce   bool
	closePending bool
	closeStarted bool
}

func newUpdateCloseCoordinator(manager updateCloseManager, emit func(CloseGuardEvent), close func()) *updateCloseCoordinator {
	return &updateCloseCoordinator{
		manager: manager,
		emit:    emit,
		close:   close,
	}
}

// HandleCloseRequest returns true only when Wails may continue closing. A
// successful CancelUpdateAndClose arms a one-shot bypass so the close request
// it initiates does not reopen the confirmation dialog.
func (coordinator *updateCloseCoordinator) HandleCloseRequest() bool {
	if coordinator == nil || coordinator.manager == nil {
		return true
	}

	coordinator.mu.Lock()
	if coordinator.bypassOnce {
		coordinator.bypassOnce = false
		coordinator.mu.Unlock()
		return true
	}
	coordinator.mu.Unlock()

	policy := coordinator.manager.UpdateClosePolicy()
	if policy == updateCloseAllow {
		return true
	}

	if policy == updateCloseConfirmCancel {
		coordinator.emitGuard(closeGuardConfirmCancel)
	} else {
		coordinator.emitGuard(closeGuardBlocked)
	}
	return false
}

func (coordinator *updateCloseCoordinator) emitGuard(mode CloseGuardMode) {
	if coordinator == nil || coordinator.manager == nil || coordinator.emit == nil {
		return
	}
	coordinator.emit(CloseGuardEvent{
		Mode:          mode,
		ProgressPhase: coordinator.manager.State().ProgressPhase,
	})
}

func (coordinator *updateCloseCoordinator) CancelUpdateAndClose() (resultErr error) {
	if coordinator == nil || coordinator.manager == nil {
		return errors.New("update close coordinator is unavailable")
	}

	coordinator.mu.Lock()
	if coordinator.closePending || coordinator.closeStarted {
		coordinator.mu.Unlock()
		return nil
	}
	coordinator.closePending = true
	coordinator.mu.Unlock()
	defer func() {
		if resultErr == nil {
			return
		}
		coordinator.mu.Lock()
		coordinator.closePending = false
		coordinator.mu.Unlock()
	}()

	switch coordinator.manager.UpdateClosePolicy() {
	case updateCloseAllow:
		// The update may have completed while the confirmation dialog was open.
	case updateCloseConfirmCancel:
		// CancelUpdate performs its own policy check so a transition into the
		// critical phase cannot be interrupted by a stale dialog action.
		if err := coordinator.manager.CancelUpdate(); err != nil {
			if coordinator.manager.UpdateClosePolicy() == updateCloseBlock {
				coordinator.emitGuard(closeGuardBlocked)
			}
			return err
		}
		coordinator.manager.WaitForIdle()
	default:
		coordinator.emitGuard(closeGuardBlocked)
		return errors.New("已開始套用或復原遊戲版本，現在無法關閉 Launcher")
	}

	// Recheck after cancellation and waiting: the update may have entered its
	// critical phase just before the cancellation request was observed.
	if coordinator.manager.UpdateClosePolicy() != updateCloseAllow {
		coordinator.emitGuard(closeGuardBlocked)
		return errors.New("更新已進入不可中斷的階段，現在無法關閉 Launcher")
	}

	coordinator.mu.Lock()
	if coordinator.closeStarted {
		coordinator.closePending = false
		coordinator.mu.Unlock()
		return nil
	}
	coordinator.closePending = false
	coordinator.closeStarted = true
	coordinator.bypassOnce = true
	closeApplication := coordinator.close
	coordinator.mu.Unlock()

	if closeApplication == nil {
		coordinator.mu.Lock()
		coordinator.closeStarted = false
		coordinator.bypassOnce = false
		coordinator.mu.Unlock()
		return errors.New("application close handler is unavailable")
	}
	closeApplication()
	return nil
}
