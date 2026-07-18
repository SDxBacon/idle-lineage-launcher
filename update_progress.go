package main

import (
	"errors"
	"time"
)

const (
	updateProgressSteps = 4
	updateWaitHintDelay = 10 * time.Second
	updateSlowHintDelay = 30 * time.Second
	updateWaitHintText  = "仍在等待 GitHub 回應…"
	updateSlowHintText  = "GitHub 回應較慢，更新仍在進行；你可以取消後重試。"
)

type updateClosePolicy uint8

const (
	updateCloseAllow updateClosePolicy = iota
	updateCloseConfirmCancel
	updateCloseBlock
)

type gameOperationProgress struct {
	sequence     uint64
	started      time.Time
	lastActivity time.Time
	network      bool
	stop         chan struct{}
}

func (m *gameManager) startUpdateProgressLocked(now time.Time) {
	m.stopOperationProgressLocked()
	m.progressSequence++
	progress := &gameOperationProgress{
		sequence:     m.progressSequence,
		started:      now,
		lastActivity: now,
		network:      true,
		stop:         make(chan struct{}),
	}
	m.operationProgress = progress
	m.state.ProgressStep = 1
	m.state.ProgressStepTotal = updateProgressSteps
	m.state.ProgressCancellable = true
	go m.runOperationHeartbeat(progress)
}

func (m *gameManager) stopOperationProgressLocked() {
	if m.operationProgress == nil {
		return
	}
	close(m.operationProgress.stop)
	m.operationProgress = nil
}

func (m *gameManager) runOperationHeartbeat(progress *gameOperationProgress) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			m.publishOperationHeartbeat(progress.sequence, now)
		case <-progress.stop:
			return
		}
	}
}

func (m *gameManager) publishOperationHeartbeat(sequence uint64, now time.Time) {
	m.mu.Lock()
	progress := m.operationProgress
	if progress == nil || progress.sequence != sequence || (m.state.Status != StatusUpdating && m.state.Status != StatusRecovering) {
		m.mu.Unlock()
		return
	}
	m.state.ProgressSeconds = elapsedSeconds(progress.started, now)
	if progress.network {
		idle := now.Sub(progress.lastActivity)
		switch {
		case idle >= updateSlowHintDelay:
			m.state.ProgressText = updateSlowHintText
		case idle >= updateWaitHintDelay:
			m.state.ProgressText = updateWaitHintText
		}
	}
	m.advanceRevisionLocked()
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func elapsedSeconds(started, now time.Time) int64 {
	if started.IsZero() || now.Before(started) {
		return 0
	}
	return int64(now.Sub(started).Seconds())
}

func (m *gameManager) setUpdateProgress(step int, phase, text string, percent int, cancellable, network bool) {
	now := time.Now()
	m.mu.Lock()
	if m.state.Status != StatusUpdating || m.operationProgress == nil {
		m.mu.Unlock()
		return
	}
	if step < 1 {
		step = 1
	}
	if step > updateProgressSteps {
		step = updateProgressSteps
	}
	m.state.ProgressStep = step
	m.state.ProgressStepTotal = updateProgressSteps
	m.state.ProgressPhase = phase
	m.state.ProgressText = text
	m.state.ProgressPercent = percent
	m.state.ProgressCancellable = cancellable
	m.state.ProgressSeconds = elapsedSeconds(m.operationProgress.started, now)
	m.operationProgress.network = network
	m.operationProgress.lastActivity = now
	m.advanceRevisionLocked()
	state := m.state
	m.mu.Unlock()
	m.publish(state)
}

func (m *gameManager) noteGitProgressActivity(download bool) {
	m.mu.Lock()
	if m.operationProgress != nil && m.state.Status == StatusUpdating && m.state.ProgressCancellable {
		m.operationProgress.lastActivity = time.Now()
		m.operationProgress.network = true
		if download && m.state.ProgressStep < 2 {
			m.state.ProgressStep = 2
			m.state.ProgressPhase = "下載更新檔案"
		}
	}
	m.mu.Unlock()
}

func (m *gameManager) operationElapsed(fallback int64) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.operationProgress == nil {
		return fallback
	}
	return elapsedSeconds(m.operationProgress.started, time.Now())
}

func (m *gameManager) hasOperationProgress() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.operationProgress != nil
}

func (m *gameManager) clearProgressLocked() {
	m.state.ProgressPhase = ""
	m.state.ProgressText = ""
	m.state.ProgressPercent = 0
	m.state.ProgressSeconds = 0
	m.state.ProgressStep = 0
	m.state.ProgressStepTotal = 0
	m.state.ProgressCancellable = false
}

func (m *gameManager) CancelUpdate() error {
	m.mu.RLock()
	if m.state.Status != StatusUpdating {
		m.mu.RUnlock()
		return errors.New("目前沒有正在進行的更新")
	}
	if !m.state.ProgressCancellable {
		m.mu.RUnlock()
		return errors.New("已開始套用遊戲版本，現在無法取消更新")
	}
	cancel := m.cancel
	if cancel != nil {
		cancel()
	}
	m.mu.RUnlock()
	return nil
}

func (m *gameManager) UpdateClosePolicy() updateClosePolicy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch m.state.Status {
	case StatusRecovering:
		return updateCloseBlock
	case StatusUpdating:
		if m.state.ProgressCancellable {
			return updateCloseConfirmCancel
		}
		return updateCloseBlock
	default:
		return updateCloseAllow
	}
}

func (m *gameManager) WaitForIdle() {
	m.wg.Wait()
}
