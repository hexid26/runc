// +build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"

	"github.com/containerd/console"
	"github.com/opencontainers/runc/libcontainer"
	"github.com/opencontainers/runc/libcontainer/utils"
	"github.com/sirupsen/logrus"
)

type tty struct {
	epoller   *console.Epoller
	console   *console.EpollConsole
	stdin     console.Console
	closers   []io.Closer
	postStart []io.Closer
	wg        sync.WaitGroup
	consoleC  chan error
}

func (t *tty) copyIO(w io.Writer, r io.ReadCloser) {
	defer t.wg.Done()
	io.Copy(w, r)
  logrus.Debugf("warning::haixiang::tty.go copyIO w=%v r=%v", w, r)
	r.Close()
}

// setup pipes for the process so that advanced features like c/r are able to easily checkpoint
// and restore the process's IO without depending on a host specific path or device
func setupProcessPipes(p *libcontainer.Process, rootuid, rootgid int) (*tty, error) {
	i, err := p.InitializeIO(rootuid, rootgid)
	logrus.Debugf("warning::haixiang::tty.go setupProcessPipes i=%v", i)
	if err != nil {
		return nil, err
	}
	t := &tty{
		closers: []io.Closer{
			i.Stdin,
			i.Stdout,
			i.Stderr,
		},
	}
	// add the process's io to the post start closers if they support close
	for _, cc := range []interface{}{
		p.Stdin,
		p.Stdout,
		p.Stderr,
	} {
		if c, ok := cc.(io.Closer); ok {
			t.postStart = append(t.postStart, c)
			logrus.Debugf("warning::haixiang::tty.go setupProcessPipes t.postStart=%v", t.postStart)
		}
	}
	go func() {
		io.Copy(i.Stdin, os.Stdin)
		i.Stdin.Close()
	}()
	t.wg.Add(2)
	logrus.Debugf("warning::haixiang::tty.go setupProcessPipes os.Stdout=%v", os.Stdout)
	go t.copyIO(os.Stdout, i.Stdout)
	logrus.Debugf("warning::haixiang::tty.go setupProcessPipes os.Stderr=%v", os.Stderr)
	go t.copyIO(os.Stderr, i.Stderr)
	return t, nil
}

func inheritStdio(process *libcontainer.Process) error {
	process.Stdin = os.Stdin
	process.Stdout = os.Stdout
	process.Stderr = os.Stderr
	logrus.Debug("warning::haixiang::tty.go inheritStdio run")
	return nil
}

// ! 检查 tty
func (t *tty) recvtty(process *libcontainer.Process, socket *os.File) (Err error) {
	f, err := utils.RecvFd(socket)
  logrus.Debugf("warning::haixiang::tty.go recvtty f=%v, socket=%v", f, socket)
	if err != nil {
		return err
	}
	cons, err := console.ConsoleFromFile(f)
  logrus.Debugf("warning::haixiang::tty.go recvtty cons=%v", cons)
	if err != nil {
		return err
	}
	console.ClearONLCR(cons.Fd())
	epoller, err := console.NewEpoller()
  logrus.Debugf("warning::haixiang::tty.go recvtty epoller=%v", epoller)
	if err != nil {
		return err
	}
	epollConsole, err := epoller.Add(cons)
  logrus.Debugf("warning::haixiang::tty.go recvtty epollConsole=%v", epollConsole)
	if err != nil {
		return err
	}
	defer func() {
		if Err != nil {
			epollConsole.Close()
		}
	}()
	go epoller.Wait()
	go io.Copy(epollConsole, os.Stdin)
  logrus.Debugf("warning::haixiang::tty.go recvtty t.wg=%v Ready to exec t.wg.Add(1)", t.wg)
	t.wg.Add(1)
	go t.copyIO(os.Stdout, epollConsole)

	// set raw mode to stdin and also handle interrupt
	stdin, err := console.ConsoleFromFile(os.Stdin)
	logrus.Debugf("warning::haixiang::tty.go recvtty stdin=%v, err=%v", stdin, err)
	if err != nil {
		return err
	}
	if err := stdin.SetRaw(); err != nil {
		logrus.Errorf("failed to set the terminal from the stdin: %v", err)
		return fmt.Errorf("failed to set the terminal from the stdin: %v", err)
	}
	go handleInterrupt(stdin)

	t.epoller = epoller
	t.stdin = stdin
	t.console = epollConsole
	t.closers = []io.Closer{epollConsole}
	return nil
}

func handleInterrupt(c console.Console) {
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, os.Interrupt)
	<-sigchan
	c.Reset()
	logrus.Debug("haixiang::tty.go handleInterrupt run")
	os.Exit(0)
}

func (t *tty) waitConsole() error {
	if t.consoleC != nil {
		return <-t.consoleC
	}
	logrus.Debug("haixiang::tty.go waitConsole run")
	return nil
}

// ClosePostStart closes any fds that are provided to the container and dup2'd
// so that we no longer have copy in our process.
func (t *tty) ClosePostStart() error {
	for _, c := range t.postStart {
		c.Close()
	}
	logrus.Debug("haixiang::tty.go ClosePostStart run")
	return nil
}

// Close closes all open fds for the tty and/or restores the original
// stdin state to what it was prior to the container execution
func (t *tty) Close() error {
	// ensure that our side of the fds are always closed
	logrus.Debug("haixiang::tty.go Close run")
	for _, c := range t.postStart {
		c.Close()
	}
	// the process is gone at this point, shutting down the console if we have
	// one and wait for all IO to be finished
	if t.console != nil && t.epoller != nil {
		t.console.Shutdown(t.epoller.CloseConsole)
	}
	t.wg.Wait()
	for _, c := range t.closers {
		c.Close()
	}
	if t.stdin != nil {
		t.stdin.Reset()
	}
	return nil
}

func (t *tty) resize() error {
	logrus.Debug("haixiang::tty.go resize run")
	if t.console == nil {
    logrus.Debugf("warning::haixiang::tty.go resize t.console=%v", t.console)
		return nil
	}
	return t.console.ResizeFrom(console.Current())
}
