package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
)

// Progress belongs on the terminal, never in redirected reports or diagnostics.
func progressEnabled(quiet bool) bool {
	terminal := func(f *os.File) bool { return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()) }
	return !quiet && os.Getenv("TERM") != "dumb" && terminal(os.Stdout) && terminal(os.Stderr)
}

type progress struct {
	updates chan string
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func startProgress(w io.Writer, enabled bool, label string) *progress {
	if !enabled {
		return nil
	}
	p := &progress{updates: make(chan string), stop: make(chan struct{}), done: make(chan struct{})}
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	draw := func(frame int, label string) { fmt.Fprintf(w, "\r\x1b[2K  %c %s", frames[frame], label) }
	draw(0, label)
	go func() {
		defer close(p.done)
		defer fmt.Fprint(w, "\r\x1b[2K")
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		frame := 0
		for {
			select {
			case <-p.stop:
				return
			case label = <-p.updates:
			case <-ticker.C:
				frame = (frame + 1) % len(frames)
			}
			draw(frame, label)
		}
	}()
	return p
}

func (p *progress) Update(label string) {
	if p == nil {
		return
	}
	select {
	case <-p.done:
	case p.updates <- label:
	}
}

// Stop waits for the final erase before the caller prints a report or error.
func (p *progress) Stop() {
	if p == nil {
		return
	}
	p.once.Do(func() { close(p.stop) })
	<-p.done
}
