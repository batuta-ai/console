//go:build race

package tui

import "time"

const terminalThroughputDeadline = 120 * time.Second
