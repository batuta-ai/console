package session

func InspectProcess(pid int) (ProcessObservation, error) {
	return inspectProc("/proc", pid)
}
