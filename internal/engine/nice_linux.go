//go:build linux

package engine

import (
	"runtime"
	"syscall"
)

// withNice runs fn on a locked OS thread whose scheduling niceness is raised to
// `nice` (higher = lower CPU priority), then restores the prior value. sherpa-onnx
// / onnxruntime build their intra-op worker thread pool during model construction
// on the calling thread; on Linux those workers inherit the caller's niceness, so
// constructing the models under a raised nice yields a persistently low-priority
// pool that the HTTP-serving goroutines (nice 0) preempt under CPU contention.
// nice <= 0 is a no-op. Best-effort: any syscall error falls back to running fn
// at the default priority.
func withNice(nice int, fn func()) {
	if nice <= 0 {
		fn()
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	tid := syscall.Gettid()
	// Linux getpriority returns 20-nice (so the syscall value stays non-negative).
	prev, err := syscall.Getpriority(syscall.PRIO_PROCESS, tid)
	if err != nil {
		fn()
		return
	}
	prevNice := 20 - prev
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, tid, nice); err != nil {
		fn()
		return
	}
	defer syscall.Setpriority(syscall.PRIO_PROCESS, tid, prevNice)
	fn()
}
