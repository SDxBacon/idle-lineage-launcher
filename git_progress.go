package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var gitPercentPattern = regexp.MustCompile(`(?i)^([^:]+):\s*([0-9]{1,3})%`)

const (
	gitPackfileReceivePhase = "接收 Git objects"
	gitPackfileReceiveText  = "正在下載並寫入 Git packfile"
)

const gitPackfileReceiveDelay = 1500 * time.Millisecond

type gitProgressReporter struct {
	manager   *gameManager
	operation string
	started   time.Time
	stop      chan struct{}
	closeOnce sync.Once

	mu               sync.Mutex
	buffer           string
	phase            string
	text             string
	percent          int
	lastGitOutput    time.Time
	packDir          string
	packBaseline     int64
	syntheticReceive bool
	lastLoggedPhase  string
	lastLoggedBucket int
}

func newGitProgressReporter(manager *gameManager, operation, initialText string) *gitProgressReporter {
	reporter := &gitProgressReporter{
		manager:          manager,
		operation:        operation,
		started:          time.Now(),
		stop:             make(chan struct{}),
		percent:          -1,
		lastLoggedBucket: -1,
	}
	reporter.Stage("連線", initialText)
	go reporter.heartbeat()
	return reporter
}

func (reporter *gitProgressReporter) WatchPackDir(packDir string) {
	size, _ := directoryRegularFileSize(packDir)
	reporter.mu.Lock()
	reporter.packDir = packDir
	reporter.packBaseline = size
	reporter.mu.Unlock()
}

func (reporter *gitProgressReporter) Write(contents []byte) (int, error) {
	reporter.mu.Lock()
	combined := reporter.buffer + strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(contents))
	lines := strings.Split(combined, "\n")
	reporter.buffer = lines[len(lines)-1]
	complete := append([]string(nil), lines[:len(lines)-1]...)
	reporter.mu.Unlock()

	for _, line := range complete {
		reporter.handleLine(line)
	}
	return len(contents), nil
}

func (reporter *gitProgressReporter) Stage(phase, text string) {
	text = strings.TrimSpace(text)
	reporter.mu.Lock()
	reporter.phase = phase
	reporter.text = text
	reporter.percent = -1
	elapsed := int64(time.Since(reporter.started).Seconds())
	reporter.mu.Unlock()
	reporter.manager.logger.Info("git stage", "operation", reporter.operation, "phase", phase, "detail", text)
	reporter.manager.updateGitProgress(phase, text, -1, elapsed, true)
}

func (reporter *gitProgressReporter) Close() {
	reporter.closeOnce.Do(func() {
		close(reporter.stop)
		reporter.mu.Lock()
		remaining := reporter.buffer
		reporter.buffer = ""
		reporter.mu.Unlock()
		if strings.TrimSpace(remaining) != "" {
			reporter.handleLine(remaining)
		}
	})
}

func (reporter *gitProgressReporter) handleLine(line string) {
	text := strings.TrimSpace(line)
	if text == "" {
		return
	}
	phase, percent := parseGitProgress(text)
	now := time.Now()

	reporter.mu.Lock()
	if phase == "" {
		phase = reporter.phase
	}
	reporter.phase = phase
	reporter.text = text
	reporter.percent = percent
	reporter.lastGitOutput = now
	reporter.syntheticReceive = false
	elapsed := int64(now.Sub(reporter.started).Seconds())
	bucket := -1
	if percent >= 0 {
		bucket = percent / 10
	}
	shouldLog := phase != reporter.lastLoggedPhase || (bucket >= 0 && bucket != reporter.lastLoggedBucket)
	if shouldLog {
		reporter.lastLoggedPhase = phase
		reporter.lastLoggedBucket = bucket
	}
	reporter.mu.Unlock()

	if shouldLog {
		reporter.manager.logger.Info("git progress", "operation", reporter.operation, "phase", phase, "percent", percent, "detail", text)
	}
	reporter.manager.updateGitProgress(phase, text, percent, elapsed, false)
}

func (reporter *gitProgressReporter) heartbeat() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			if reporter.maybeShowSyntheticPackfileReceive(now) {
				continue
			}
			reporter.mu.Lock()
			phase := reporter.phase
			text := reporter.text
			percent := reporter.percent
			elapsed := int64(now.Sub(reporter.started).Seconds())
			reporter.mu.Unlock()
			if text == "" {
				text = "等待 Git server 回應…"
			}
			reporter.manager.updateGitProgress(phase, text, percent, elapsed, true)
		case <-reporter.stop:
			return
		}
	}
}

func (reporter *gitProgressReporter) maybeShowSyntheticPackfileReceive(now time.Time) bool {
	reporter.mu.Lock()
	alreadySynthetic := reporter.syntheticReceive
	shouldStartSynthetic := reporter.phase == "壓縮 Git objects" &&
		reporter.percent == 100 &&
		!reporter.lastGitOutput.IsZero() &&
		now.Sub(reporter.lastGitOutput) >= gitPackfileReceiveDelay
	if !alreadySynthetic && !shouldStartSynthetic {
		reporter.mu.Unlock()
		return false
	}
	packDir := reporter.packDir
	packBaseline := reporter.packBaseline
	reporter.phase = gitPackfileReceivePhase
	reporter.text = gitPackfileReceiveText + "…"
	reporter.percent = -1
	reporter.syntheticReceive = true
	if shouldStartSynthetic {
		reporter.lastLoggedPhase = gitPackfileReceivePhase
	}
	reporter.lastLoggedBucket = -1
	elapsed := int64(now.Sub(reporter.started).Seconds())
	reporter.mu.Unlock()

	text := reporter.packfileReceiveText(packDir, packBaseline)
	if shouldStartSynthetic {
		reporter.manager.logger.Info("git stage", "operation", reporter.operation, "phase", gitPackfileReceivePhase, "detail", text)
	}
	reporter.manager.updateGitProgress(gitPackfileReceivePhase, text, -1, elapsed, true)
	return true
}

func (reporter *gitProgressReporter) packfileReceiveText(packDir string, baseline int64) string {
	written := int64(0)
	if packDir != "" {
		if size, err := directoryRegularFileSize(packDir); err == nil && size > baseline {
			written = size - baseline
		}
	}
	if written <= 0 {
		return gitPackfileReceiveText + "…"
	}
	return fmt.Sprintf("%s… 本次已接收/寫入 %s", gitPackfileReceiveText, formatByteSize(written))
}

func directoryRegularFileSize(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var size int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
	}
	return size, nil
}

func formatByteSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, suffix := range []string{"KiB", "MiB", "GiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f TiB", value/unit)
}

func parseGitProgress(text string) (string, int) {
	matches := gitPercentPattern.FindStringSubmatch(text)
	if len(matches) != 3 {
		if separator := strings.IndexByte(text, ':'); separator > 0 {
			return localiseGitPhase(strings.TrimSpace(text[:separator])), -1
		}
		return "", -1
	}
	percent, err := strconv.Atoi(matches[2])
	if err != nil {
		return localiseGitPhase(matches[1]), -1
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return localiseGitPhase(matches[1]), percent
}

func localiseGitPhase(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "enumerating objects":
		return "列舉 Git objects"
	case "counting objects":
		return "計算 Git objects"
	case "compressing objects":
		return "壓縮 Git objects"
	case "receiving objects":
		return "接收 Git objects"
	case "resolving deltas":
		return "套用 Git deltas"
	case "updating files":
		return "更新檔案"
	default:
		return strings.TrimSpace(phase)
	}
}
