//go:build !unix

package updater

import (
	"fmt"
	"os"
	"sync"
)

var fallbackUpdateLock sync.Mutex

func acquireProcessLock(path string) (func(), error) {
	fallbackUpdateLock.Lock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		fallbackUpdateLock.Unlock()
		return nil, fmt.Errorf("open updater lock: %w", err)
	}
	return func() {
		_ = file.Close()
		fallbackUpdateLock.Unlock()
	}, nil
}
