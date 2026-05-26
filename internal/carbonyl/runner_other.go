//go:build !linux

package carbonyl

import (
	"context"
	"runtime"
)

// Launch is a stub on non-Linux platforms. The real implementation
// (runner_linux.go) needs unix.IoctlSetWinsize + syscall.SysProcAttr's
// Setsid/Setctty/Ctty fields, which only exist on Linux. Returning a
// LaunchError keeps the cross-platform consumers (cmd/nightms, the
// transport closure, the browser screen's R hotkey) compiling on Windows
// for the dev loop while preventing accidental "real" use.
//
// Carbonyl itself only ships as a Linux binary (the bundle is
// linux-x86_64), so even if we wired the syscall surface for darwin/
// windows the child process couldn't actually run anyway.
func (r *Runner) Launch(ctx context.Context, req LaunchRequest) error {
	_ = ctx
	_ = req
	return &LaunchError{
		Reason: RejectInternal,
		Err:    errUnsupportedPlatform{os: runtime.GOOS},
	}
}

type errUnsupportedPlatform struct{ os string }

func (e errUnsupportedPlatform) Error() string {
	return "carbonyl rich mode requires Linux, this is " + e.os
}
