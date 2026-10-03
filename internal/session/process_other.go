//go:build !darwin && !linux

package session

import "fmt"

func InspectProcess(pid int) (ProcessObservation, error) {
	return ProcessObservation{}, fmt.Errorf("process incarnation inspection unsupported on this OS")
}
