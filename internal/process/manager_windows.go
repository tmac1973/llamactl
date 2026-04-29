//go:build windows

package process

import "os"

// terminate stops the process. Windows has no SIGTERM, so this is a hard
// kill — equivalent to TerminateProcess. Children that need graceful
// shutdown on Windows should use a higher-level mechanism (e.g. a console
// control event handler), which is out of scope here.
func terminate(p *os.Process) error {
	return p.Kill()
}
