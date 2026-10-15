package journal

import (
	"os"
	"time"
)

func lockJournal(file *os.File) (func() error, error) {
	for {
		unlock, busy, err := tryLockJournal(file)
		if err != nil || !busy {
			return unlock, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		<-timer.C
	}
}
