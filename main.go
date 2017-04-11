package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/pflag"
)

var opts = struct {
	reportAllOutput  bool
	waitBeforeDetach time.Duration
}{}

func init() {
	pflag.BoolVarP(&opts.reportAllOutput, "all", "a", false, "report all output after exit")
	pflag.DurationVarP(&opts.waitBeforeDetach, "wait-before-detach", "w", 10*time.Second, "show output before detaching")
	pflag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: later [options] [--] cmd [arg]...\n\nOptions:\n")
		pflag.PrintDefaults()
	}
	pflag.Parse()
}

// Command bundles all data needed for running one command.
type Command struct {
	start time.Time
	*exec.Cmd

	status    syscall.WaitStatus
	exitError error
	exited    chan struct{}

	detachDelay time.Duration

	detachMutex sync.Mutex
	detached    bool

	output *bytes.Buffer
}

// Run starts the program.
func (c *Command) Run() error {
	c.exited = make(chan struct{})
	c.output = bytes.NewBuffer(nil)

	c.Cmd.Stderr = os.Stderr

	stdout, err := c.Cmd.StdoutPipe()
	if err != nil {
		return err
	}

	go c.readOutput(stdout)
	go c.detachAfter(c.detachDelay)

	err = c.Cmd.Start()
	if err != nil {
		return err
	}

	go c.wait()
	return nil
}

func (c *Command) detachAfter(d time.Duration) {
	t := time.NewTimer(d)
	<-t.C

	fmt.Printf("%v detaching\n", time.Now())

	c.detachMutex.Lock()
	c.detached = true
	c.detachMutex.Unlock()
}

func (c *Command) readOutput(rd io.ReadCloser) {
	buf := make([]byte, 1*1024*1024)
	for {
		buf = buf[:cap(buf)]
		n, err := rd.Read(buf)
		buf = buf[:n]

		c.detachMutex.Lock()
		detached := c.detached
		c.detachMutex.Unlock()

		if detached || opts.reportAllOutput {
			_, err := c.output.Write(buf)
			if err != nil {
				panic(err)
			}
		}

		if !detached {
			_, err := os.Stdout.Write(buf)
			if err != nil {
				panic(err)
			}
		}

		if err == io.EOF {
			return
		}

		if err != nil {
			panic(err)
		}
	}
}

// wait blocks until the command exits.
func (c *Command) wait() error {
	defer close(c.exited)
	err := c.Cmd.Wait()

	c.exitError = err

	if _, ok := err.(*exec.ExitError); ok {
		return nil
	}

	return err
}

// WaitForExitCode returns the exit code of the command.
func (c *Command) WaitForExitCode() int {
	<-c.exited

	if c.exitError == nil {
		return 0
	}

	if e, ok := c.exitError.(*exec.ExitError); ok {
		if s, ok := e.Sys().(syscall.WaitStatus); ok {
			c.status = s
			return s.ExitStatus()
		}
	}

	return 0
}

func main() {
	args := pflag.Args()
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "no command given\n")
		pflag.Usage()
		os.Exit(1)
	}

	cmdName, args := args[0], args[1:]

	fmt.Printf("running: %v %v\n", cmdName, strings.Join(args, " "))

	cmd := &Command{
		Cmd:         exec.Command(cmdName, args...),
		start:       time.Now(),
		detachDelay: opts.waitBeforeDetach,
	}

	err := cmd.Run()
	if err != nil {
		panic(err)
	}

	exitCode := cmd.WaitForExitCode()

	os.Stdout.Write(cmd.output.Bytes())

	fmt.Printf("program terminated (exit code %d) at %v (runtime %v)\n",
		exitCode, time.Now(), time.Since(cmd.start))
}
