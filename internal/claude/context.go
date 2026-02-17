package claude

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// ContextMonitor tracks estimated token usage to detect when context
// compaction is needed.
type ContextMonitor struct {
	estimatedTokens     atomic.Int64
	maxTokens           int64
	compactionThreshold float64
}

// NewContextMonitor creates a ContextMonitor.
// threshold is a fraction (e.g. 0.8 for 80%) at which compaction is recommended.
func NewContextMonitor(maxTokens int64, threshold float64) *ContextMonitor {
	return &ContextMonitor{
		maxTokens:           maxTokens,
		compactionThreshold: threshold,
	}
}

// TrackInput adds an estimated token count based on input data length.
// Rough estimate: 1 token per 4 bytes.
func (cm *ContextMonitor) TrackInput(data []byte) {
	tokens := int64(len(data) / 4)
	if tokens == 0 {
		tokens = 1
	}
	cm.estimatedTokens.Add(tokens)
}

// TrackOutput adds an estimated token count based on output data length.
func (cm *ContextMonitor) TrackOutput(data []byte) {
	tokens := int64(len(data) / 4)
	if tokens == 0 {
		tokens = 1
	}
	cm.estimatedTokens.Add(tokens)
}

// UsagePercent returns the estimated percentage of context window used.
func (cm *ContextMonitor) UsagePercent() int {
	if cm.maxTokens == 0 {
		return 0
	}
	pct := float64(cm.estimatedTokens.Load()) / float64(cm.maxTokens) * 100
	if pct > 100 {
		return 100
	}
	return int(pct)
}

// NeedsCompaction returns true if estimated usage exceeds the threshold.
func (cm *ContextMonitor) NeedsCompaction() bool {
	if cm.maxTokens == 0 {
		return false
	}
	ratio := float64(cm.estimatedTokens.Load()) / float64(cm.maxTokens)
	return ratio >= cm.compactionThreshold
}

// Reset clears the estimated token count (used after compaction).
func (cm *ContextMonitor) Reset() {
	cm.estimatedTokens.Store(0)
}

// GenerateResumptionPrompt creates a prompt that helps a restarted agent
// pick up where it left off.
func GenerateResumptionPrompt(originalTask, progress string, modifiedFiles []string) string {
	var b strings.Builder

	b.WriteString("You are resuming a task after a context compaction. Here is the context:\n\n")
	b.WriteString(fmt.Sprintf("## Original Task\n%s\n\n", originalTask))
	b.WriteString(fmt.Sprintf("## Progress So Far\n%s\n\n", progress))

	if len(modifiedFiles) > 0 {
		b.WriteString("## Modified Files\n")
		for _, f := range modifiedFiles {
			b.WriteString(fmt.Sprintf("- %s\n", f))
		}
		b.WriteString("\n")
	}

	b.WriteString("Continue from where you left off. Review the modified files to understand current state.\n")

	return b.String()
}
