//go:build !darwin

package overlay

// platformStart is a no-op on non-macOS platforms.
func (o *Overlay) platformStart() error {
	return nil
}

// platformStop is a no-op on non-macOS platforms.
func (o *Overlay) platformStop() {
}

// platformSetEnabled is a no-op on non-macOS platforms.
func (o *Overlay) platformSetEnabled(enabled bool) {
}

// RunMainRunLoop is a no-op on non-macOS platforms.
func RunMainRunLoop() {
}

// StopMainRunLoop is a no-op on non-macOS platforms.
func StopMainRunLoop() {
}
