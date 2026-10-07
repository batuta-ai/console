//go:build !race

package tui

import "time"

const terminalThroughputDeadline = 10 * time.Second
