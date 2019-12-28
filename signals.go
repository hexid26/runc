// +build linux

package main

import (
	"os"
	"os/signal"
	"syscall" // only for Signal

	"github.com/opencontainers/runc/libcontainer"
	"github.com/opencontainers/runc/libcontainer/system"
	"github.com/opencontainers/runc/libcontainer/utils"

	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

const signalBufferSize = 204800

// newSignalHandler returns a signal handler for processing SIGCHLD and SIGWINCH signals
// while still forwarding all other signals to the process.
// If notifySocket is present, use it to read systemd notifications from the container and
// forward them to notifySocketHost.
// * When a child process stops or terminates, SIGCHLD is sent to the parent process
// * SIGWINCH is sent to the foreground process group when the terminal window size changes
// * as vi and less.
func newSignalHandler(enableSubreaper bool, notifySocket *notifySocket) *signalHandler {
	if enableSubreaper {
		// set us as the subreaper before registering the signal handler for the container
		if err := system.SetSubreaper(1); err != nil {
			logrus.Warn(err)
		}
	}
	// ensure that we have a large buffer size so that we do not miss any signals
	// in case we are not processing them fast enough.
	s := make(chan os.Signal, signalBufferSize)
	logrus.Debugf("warning::haixiang::signals.go::newSignalHandler s=%v", s)
	// handle all signals for the process.
	signal.Notify(s)
	logrus.Debugf("warning::haixiang::signals.go::newSignalHandler enableSubreaper=%v; notifySocket=%v", enableSubreaper, notifySocket)
	return &signalHandler{
		signals:      s,
		notifySocket: notifySocket,
	}
}

// exit models a process exit status with the pid and
// exit status.
type exit struct {
	pid    int
	status int
}

type signalHandler struct {
	signals      chan os.Signal
	notifySocket *notifySocket
}

// forward handles the main signal event loop forwarding, resizing, or reaping depending
// on the signal received.
func (h *signalHandler) forward(process *libcontainer.Process, tty *tty, detach bool) (int, error) {
	// make sure we know the pid of our main process so that we can return
	// after it dies.
	logrus.Debugf("warning::haixiang::signals.go::forward process=%v; tty=%v; detach=%v", process, tty, detach)
	logrus.Debugf("warning::haixiang::signals.go::forward h.signals=%v; h.notifySocket=%v", h.signals, h.notifySocket)
	if detach && h.notifySocket == nil {
    logrus.Debugf("warning::haixiang::signals.go::forward h.notifySocket=%v", h.notifySocket)
		return 0, nil
	}

	pid1, err := process.Pid()
	if err != nil {
		return -1, err
	}
  logrus.Debugf("haixiang::signals.go::forward pid1=%v", pid1)

	if h.notifySocket != nil {
		if detach {
			h.notifySocket.run(pid1)
			return 0, nil
		}
    logrus.Debugf("warning::haixiang::signals.go::forward h.notifySocket=%v, ready for go thread", h.notifySocket)
		go h.notifySocket.run(0)
	}

  logrus.Debugf("warning::haixiang::signals.go::forward h.notifySocket=%v, Initial tty resize.", h.notifySocket)
	// ! Perform the initial tty resize. Always ignore errors resizing because
	// ! stdout might have disappeared (due to races with when SIGHUP is sent).
	_ = tty.resize()
  logrus.Debugf("warning::haixiang::signals.go::forward tty.resize=%v", tty.resize())
  // ! 上面的函数干啥的？
	// Handle and forward signals.
	for s := range h.signals {
		logrus.Debugf("warning::haixiang::signals.go::forward h=%v, s=%v", h, s)
		switch s {
		case unix.SIGWINCH:
			// ! Ignore errors resizing, as above.
			_ = tty.resize()
      logrus.Debugf("warning::haixiang::signals.go::forward get SIGWINCH")
		case unix.SIGCHLD:
			exits, err := h.reap()
			if err != nil {
				logrus.Error(err)
			}
      logrus.Debugf("warning::haixiang::signals.go::forward exits=%v", exits)
			for _, e := range exits {
				logrus.WithFields(logrus.Fields{
					"pid":    e.pid,
					"status": e.status,
				}).Debug("process exited")
				if e.pid == pid1 {
					// call Wait() on the process even though we already have the exit
					// status because we must ensure that any of the go specific process
					// fun such as flushing pipes are complete before we return.
          logrus.Debugf("warning::haixiang::signals.go::forward wait process=%v", process)
					process.Wait()
					if h.notifySocket != nil {
            logrus.Debugf("warning::haixiang::signals.go::forward close notifySocket")
						h.notifySocket.Close()
					}
          logrus.Debugf("warning::haixiang::signals.go::forward e.status=%v", e.status)
					return e.status, nil
				}
			}
		default:
			logrus.Debugf("sending signal to process %s", s)
			if err := unix.Kill(pid1, s.(syscall.Signal)); err != nil {
				logrus.Error(err)
			}
		}
	}
	return -1, nil
}

// reap runs wait4 in a loop until we have finished processing any existing exits
// then returns all exits to the main event loop for further processing.
func (h *signalHandler) reap() (exits []exit, err error) {
	var (
		ws  unix.WaitStatus
		rus unix.Rusage
	)
	for {
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, &rus)
		if err != nil {
			if err == unix.ECHILD {
				return exits, nil
			}
			return nil, err
		}
		if pid <= 0 {
			return exits, nil
		}
		exits = append(exits, exit{
			pid:    pid,
			status: utils.ExitStatus(ws),
		})
	}
}
