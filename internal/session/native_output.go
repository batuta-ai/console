package session

type nativeOutput struct {
	limit     int64
	data      []byte
	truncated bool
}

func (w *nativeOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - int64(len(w.data))
	if int64(n) > remaining {
		w.truncated = true
	}
	if remaining > 0 {
		if int64(len(p)) > remaining {
			p = p[:remaining]
		}
		w.data = append(w.data, p...)
	}
	return n, nil
}
