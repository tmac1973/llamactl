//go:build !windows

package process

import (
	"os"
	"syscall"
)

// terminate asks the process to exit gracefully (SIGTERM on unix).
func terminate(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
