package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Defaults for showing logs.
const (
	defaultTailLines = 200
	// tailReadChunk is how many bytes are read back at once to find the last
	// N lines.
	tailReadChunk = 64 * 1024
	// followInterval is the polling interval of -f.
	followInterval = 300 * time.Millisecond
)

// cmdLogs shows <state>/daemon.log. With -f it follows the file.
func cmdLogs(args []string) int {
	fs := newFlagSet("logs")
	follow := fs.Bool("f", false, "follow appended output")
	lines := fs.Int("n", defaultTailLines, "number of trailing lines to show (0 for all)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: localapp logs [-f] [-n lines]")
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(pos) != 0 {
		fs.Usage()
		return exitUsage
	}
	if *lines < 0 {
		errf("-n takes a value of 0 or more")
		return exitUsage
	}

	path := loadConfig().LogPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			errf("no log yet (%s); the daemon may never have run", path)
			return exitError
		}
		return reportError(fmt.Errorf("cannot open the log (%s): %w", path, err))
	}
	defer f.Close()

	offset, err := writeTail(f, os.Stdout, *lines)
	if err != nil {
		return reportError(err)
	}
	if !*follow {
		return exitOK
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := followFile(ctx, path, os.Stdout, offset, followInterval); err != nil {
		return reportError(err)
	}
	return exitOK
}

// writeTail writes the last n lines of f to w and returns the offset of the
// end of the file. With n == 0 it writes everything.
func writeTail(f *os.File, w io.Writer, n int) (int64, error) {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, fmt.Errorf("reading the log: %w", err)
	}
	readFrom := int64(0)
	if n > 0 && size > tailReadChunk {
		readFrom = size - tailReadChunk
	}
	buf := make([]byte, size-readFrom)
	if _, err := f.ReadAt(buf, readFrom); err != nil && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("reading the log: %w", err)
	}
	if _, err := w.Write(lastLines(buf, n)); err != nil {
		return 0, err
	}
	return size, nil
}

// lastLines returns the last n lines of data. With n <= 0 it returns all of
// data.
func lastLines(data []byte, n int) []byte {
	if n <= 0 || len(data) == 0 {
		return data
	}
	// A trailing newline terminates the last line; it is not counted as a
	// separator.
	end := len(data)
	if data[end-1] == '\n' {
		end--
	}
	count := 0
	for i := end - 1; i >= 0; i-- {
		if data[i] != '\n' {
			continue
		}
		count++
		if count == n {
			return data[i+1:]
		}
	}
	return data
}

// followFile keeps streaming to w whatever is appended past offset. It returns
// when ctx is cancelled. If the file is truncated it starts over from the
// beginning.
func followFile(ctx context.Context, path string, w io.Writer, offset int64, interval time.Duration) error {
	buf := make([]byte, 32*1024)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		var err error
		offset, err = drain(path, w, offset, buf)
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// drain writes everything past offset in path to w and returns the new offset.
func drain(path string, w io.Writer, offset int64, buf []byte) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Rotation in progress; the next poll picks it up.
			return 0, nil
		}
		return offset, fmt.Errorf("cannot open the log (%s): %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return offset, fmt.Errorf("stat of the log (%s): %w", path, err)
	}
	if info.Size() < offset {
		offset = 0 // truncated or rotated
	}
	for {
		n, err := f.ReadAt(buf, offset)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return offset, werr
			}
			offset += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return offset, nil
			}
			return offset, fmt.Errorf("reading the log (%s): %w", path, err)
		}
	}
}
