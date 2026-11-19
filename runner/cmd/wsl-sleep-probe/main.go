// wsl-sleep-probe collects clock evidence; it does not decide persistence safety.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"
)

type sample struct {
	Version  int       `json:"version"`
	Sequence int64     `json:"sequence"`
	Wall     time.Time `json:"wall"`
	BootNS   int64     `json:"boottime_ns"`
	MonoNS   int64     `json:"monotonic_ns"`
}

type clock interface {
	sample() (sample, error)
	wait(context.Context) error
}

type syncWriter interface {
	io.Writer
	Sync() error
}

func record(ctx context.Context, c clock, w syncWriter) error {
	enc := json.NewEncoder(w)
	for seq := int64(1); ; seq++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s, err := c.sample()
		if err != nil {
			return err
		}
		s.Version, s.Sequence = 1, seq
		if err := enc.Encode(s); err != nil {
			return err
		}
		if err := w.Sync(); err != nil {
			return err
		}
		if err := c.wait(ctx); err != nil {
			return err
		}
	}
}

// report prints every adjacent pair, without classifying a gap as host sleep.
func report(r io.Reader, w io.Writer) error {
	dec := json.NewDecoder(r)
	if _, err := fmt.Fprintln(w, "sample\twall_end_utc\twall_gap\tboottime_gap\tmonotonic_gap\tboot_minus_mono\twall_minus_boot"); err != nil {
		return err
	}
	var prev sample
	for seq := int64(1); ; seq++ {
		var s sample
		err := dec.Decode(&s)
		if errors.Is(err, io.EOF) {
			if seq < 3 {
				return errors.New("recording needs at least two samples to report a gap")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("sample %d: %w", seq, err)
		}
		if s.Version != 1 || s.Sequence != seq || s.Wall.IsZero() || s.BootNS <= 0 || s.MonoNS <= 0 {
			return fmt.Errorf("sample %d: invalid version, sequence or clock fields; use one complete recording", seq)
		}
		if seq > 1 {
			if s.BootNS < prev.BootNS || s.MonoNS < prev.MonoNS {
				return fmt.Errorf("sample %d: clock moved backwards; recording may span a restart", seq)
			}
			wall := s.Wall.Sub(prev.Wall)
			boot, mono := time.Duration(s.BootNS-prev.BootNS), time.Duration(s.MonoNS-prev.MonoNS)
			if _, err := fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", seq, s.Wall.UTC().Format(time.RFC3339Nano), wall, boot, mono, boot-mono, wall-boot); err != nil {
				return err
			}
		}
		prev = s
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: wsl-sleep-probe record -out FILE | read FILE")
	}
	switch args[0] {
	case "record":
		flags := flag.NewFlagSet("record", flag.ContinueOnError)
		path := flags.String("out", "", "new local JSONL file (must not exist)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *path == "" || flags.NArg() != 0 {
			return errors.New("usage: wsl-sleep-probe record -out FILE")
		}
		c, err := newClock()
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		err = record(ctx, c, f)
		closeErr := f.Close()
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return closeErr
	case "read":
		if len(args) != 2 {
			return errors.New("usage: wsl-sleep-probe read FILE")
		}
		f, err := os.Open(args[1]) //nolint:gosec // Local diagnostic deliberately reads the file chosen by its operator.
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		return report(f, out)
	default:
		return errors.New("usage: wsl-sleep-probe record -out FILE | read FILE")
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
