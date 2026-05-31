package stream

import "sync"

var (
	stopStreamMu   sync.Mutex
	stopStreamChan = make(chan struct{})
)

// Остановить текущий поток (без блокировки)
func StopCurrentStream() {
	stopStreamMu.Lock()
	defer stopStreamMu.Unlock()

	close(stopStreamChan)
	stopStreamChan = make(chan struct{})
}

// Вернуть канал для StreamRadio
func StopChan() <-chan struct{} {
	stopStreamMu.Lock()
	defer stopStreamMu.Unlock()
	return stopStreamChan
}
