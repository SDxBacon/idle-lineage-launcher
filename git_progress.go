package main

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var gitPercentPattern = regexp.MustCompile(`(?i)^([^:]+):\s*([0-9]{1,3})%`)

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

	reporter.mu.Lock()
	if phase == "" {
		phase = reporter.phase
	}
	reporter.phase = phase
	reporter.text = text
	reporter.percent = percent
	elapsed := int64(time.Since(reporter.started).Seconds())
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
			reporter.mu.Lock()
			phase := reporter.phase
			text := reporter.text
			percent := reporter.percent
			elapsed := int64(time.Since(reporter.started).Seconds())
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
