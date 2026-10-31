// Package progress keeps the log of the running (or last) catalog action, which the UI
// shows in a terminal panel while it waits. Before it the page said only "Installing…":
// installs on the second test NAS took 5 to 11 s plus up to 3 s for the catalog reload (journal,
// 26–27 Oct 2026), with nothing to see in between.
//
// A line carries a text key and its values for the UI's translation and an English
// text as fallback. Download progress is one "bar" line that is replaced in place.
package progress

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxLines caps one job's log; older lines are dropped and counted.
const maxLines = 400

// Line kinds the UI colours.
const (
	Step = "step" // what Module Manager does next
	Cmd  = "cmd"  // a command it runs
	Out  = "out"  // that command's output
	Bar  = "bar"  // download progress, replaced in place
	OK   = "ok"
	Err  = "err"
)

// Line is one line of the log.
type Line struct {
	Kind string            `json:"kind"`
	Key  string            `json:"key,omitempty"`
	Args map[string]string `json:"args,omitempty"`
	Text string            `json:"text"`
	At   time.Time         `json:"at"`
}

// Snapshot is what GET /modmanager/progress returns. ID 0: no action since the start.
type Snapshot struct {
	ID      int64     `json:"id"`
	Action  string    `json:"action,omitempty"`
	Target  string    `json:"target,omitempty"`
	Running bool      `json:"running"`
	OK      bool      `json:"ok"`
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
	Rev     int64     `json:"rev"`
	Dropped int       `json:"dropped,omitempty"`
	Lines   []Line    `json:"lines"`
}

// Log holds the current job. The zero value is ready; a nil *Log ignores everything,
// so code that reports progress needs no check.
type Log struct {
	mu  sync.Mutex
	job Snapshot
	now func() time.Time
}

func (l *Log) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// Start begins a new job and forgets the previous one. The id is the start time in
// milliseconds (at least one more than the last): a page that stayed open across a
// restart of Module Manager (self-update, update by hand) knows ids of the old process
// and shows only newer ones — a counter from 1 would be hidden there (measured on the second test NAS,
// 2026-10-27: the panel stayed empty after a restart).
func (l *Log) Start(action, target string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	id := max(now.UnixMilli(), l.job.ID+1)
	l.job = Snapshot{ID: id, Action: action, Target: target, Running: true, Started: now, Rev: l.job.Rev + 1}
}

// Finish ends the job with its result; a failed one gets the error as its last line.
func (l *Log) Finish(err error) {
	if l == nil {
		return
	}
	if err != nil {
		l.add(Line{Kind: Err, Key: "failed", Args: map[string]string{"error": err.Error()}, Text: "Failed: " + err.Error()}, false)
	} else {
		l.add(Line{Kind: OK, Key: "done", Text: "Done."}, false)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.job.Running, l.job.OK, l.job.Ended = false, err == nil, l.clock()
	l.job.Rev++
}

// Snapshot returns a copy of the job.
func (l *Log) Snapshot() Snapshot {
	if l == nil {
		return Snapshot{Lines: []Line{}}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.job
	s.Lines = append([]Line{}, l.job.Lines...)
	return s
}

// Stepf reports a step: key and its values for the UI, en the English text with
// {name} placeholders filled from kv (pairs of name, value).
func (l *Log) Stepf(key, en string, kv ...string) { l.say(Step, key, en, kv) }

// Okf reports a check that passed.
func (l *Log) Okf(key, en string, kv ...string) { l.say(OK, key, en, kv) }

// Errf reports a problem that does not end the job by itself (the way back follows).
func (l *Log) Errf(key, en string, kv ...string) { l.say(Err, key, en, kv) }

func (l *Log) say(kind, key, en string, kv []string) {
	if l == nil {
		return
	}
	args := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		args[kv[i]] = kv[i+1]
		en = strings.ReplaceAll(en, "{"+kv[i]+"}", kv[i+1])
	}
	if len(args) == 0 {
		args = nil
	}
	l.add(Line{Kind: kind, Key: key, Args: args, Text: en}, false)
}

// Command reports a command line about to run.
func (l *Log) Command(name string, args ...string) {
	if l == nil {
		return
	}
	l.add(Line{Kind: Cmd, Text: strings.TrimSpace(name + " " + strings.Join(args, " "))}, false)
}

// Output reports command output, one line per line; blank lines are left out.
func (l *Log) Output(b []byte) {
	if l == nil {
		return
	}
	for _, s := range strings.Split(clean(string(b)), "\n") {
		if strings.TrimSpace(s) != "" {
			l.add(Line{Kind: Out, Text: s}, false)
		}
	}
}

// Download reports the bytes of a download so far; total < 0 when the size is unknown.
func (l *Log) Download(done, total int64) {
	if l == nil {
		return
	}
	args := map[string]string{"done": itoa(done), "total": itoa(total)}
	text := itoa(done) + " bytes"
	if total > 0 {
		text = itoa(done) + " of " + itoa(total) + " bytes (" + itoa(done*100/total) + " %)"
	}
	l.add(Line{Kind: Bar, Key: "download", Args: args, Text: text}, true)
}

// add drops a line that comes while no job runs: Reconcile at start re-merges the
// system extensions through the same code as Hide, outside any action.
func (l *Log) add(ln Line, replaceBar bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.job.Running {
		return
	}
	ln.At = l.clock()
	if n := len(l.job.Lines); replaceBar && n > 0 && l.job.Lines[n-1].Kind == Bar {
		l.job.Lines[n-1] = ln
	} else {
		l.job.Lines = append(l.job.Lines, ln)
		if len(l.job.Lines) > maxLines {
			cut := len(l.job.Lines) - maxLines
			l.job.Lines = append([]Line{}, l.job.Lines[cut:]...)
			l.job.Dropped += cut
		}
	}
	l.job.Rev++
}

// Writer returns an io.Writer that reports every complete line as command output
// (the rest when Flush is called) and keeps the last 8 KiB for an error message.
func (l *Log) Writer() *LineWriter { return &LineWriter{log: l} }

// LineWriter is the io.Writer of Writer.
type LineWriter struct {
	log  *Log
	mu   sync.Mutex
	part []byte
	tail []byte
}

var _ io.Writer = (*LineWriter)(nil)

func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tail = append(w.tail, p...)
	if len(w.tail) > 8<<10 {
		w.tail = w.tail[len(w.tail)-8<<10:]
	}
	w.part = append(w.part, p...)
	if i := bytes.LastIndexByte(w.part, '\n'); i >= 0 {
		w.log.Output(w.part[:i])
		w.part = append([]byte{}, w.part[i+1:]...)
	}
	return len(p), nil
}

// Flush reports what is left after the last newline.
func (w *LineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.part) > 0 {
		w.log.Output(w.part)
		w.part = nil
	}
}

// Tail is the end of everything written.
func (w *LineWriter) Tail() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte{}, w.tail...)
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// clean drops terminal colour codes and keeps what a carriage return left visible.
func clean(s string) string {
	s = ansi.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		ln = strings.TrimRight(ln, "\r")
		if j := strings.LastIndexByte(ln, '\r'); j >= 0 {
			ln = ln[j+1:]
		}
		lines[i] = ln
	}
	return strings.Join(lines, "\n")
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

type ctxKey struct{}

// NewContext carries a log to code that has no field for it (sysapps): only a request
// that started a job reports, never Reconcile at start.
func NewContext(ctx context.Context, l *Log) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the log NewContext put there, or nil (which ignores everything).
func FromContext(ctx context.Context) *Log {
	l, _ := ctx.Value(ctxKey{}).(*Log)
	return l
}

// Stream runs a command and writes its output to out while it runs.
type Stream func(ctx context.Context, out io.Writer, name string, args ...string) error

// ExecStream runs a command on the host, stdout and stderr into out.
func ExecStream(ctx context.Context, out io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// Exec runs a command that changes the host and reports it with its output: live
// through stream when there is one, else through run after it ended. The error
// names the command and the end of its output, as sysapps.ExecRunner does.
func (l *Log) Exec(ctx context.Context, stream Stream, run func(context.Context, string, ...string) ([]byte, error), name string, args ...string) ([]byte, error) {
	l.Command(name, args...)
	if stream == nil {
		out, err := run(ctx, name, args...)
		l.Output(out)
		return out, err
	}
	w := l.Writer()
	err := stream(ctx, w, name, args...)
	w.Flush()
	out := w.Tail()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
