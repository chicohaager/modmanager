package progress

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func kinds(s Snapshot) string {
	var k []string
	for _, l := range s.Lines {
		k = append(k, l.Kind)
	}
	return strings.Join(k, ",")
}

func TestAJobRecordsStepsAndItsResult(t *testing.T) {
	var l Log
	l.Start("install", "gh:o/r")
	l.Stepf("image", "Release {tag}: image {file}", "tag", "v1.2.0", "file", "r-amd64.raw")
	l.Command("zpkg", "install", "/x/r.raw")
	l.Output([]byte("\x1b[32minstalled\x1b[0m r\n\n"))
	l.Finish(nil)
	s := l.Snapshot()
	if s.ID != 1 || s.Running || !s.OK || s.Ended.IsZero() || s.Action != "install" || s.Target != "gh:o/r" {
		t.Fatalf("job %+v", s)
	}
	if got := kinds(s); got != "step,cmd,out,ok" {
		t.Fatalf("kinds %s", got)
	}
	if s.Lines[0].Text != "Release v1.2.0: image r-amd64.raw" || s.Lines[0].Args["tag"] != "v1.2.0" || s.Lines[0].Key != "image" {
		t.Errorf("step %+v", s.Lines[0])
	}
	if s.Lines[1].Text != "zpkg install /x/r.raw" || s.Lines[2].Text != "installed r" {
		t.Errorf("command/output %+v", s.Lines[1:3])
	}
	l.Start("uninstall", "r")
	l.Finish(errors.New("zpkg remove r: exit status 1"))
	s = l.Snapshot()
	if s.ID != 2 || s.OK || len(s.Lines) != 1 || s.Lines[0].Kind != Err || !strings.Contains(s.Lines[0].Text, "exit status 1") {
		t.Fatalf("failed job %+v", s)
	}
}

func TestTheDownloadBarIsReplacedInPlace(t *testing.T) {
	var l Log
	l.Start("install", "x")
	l.Stepf("download", "Downloading")
	l.Download(10, 100)
	l.Download(50, 100)
	l.Download(100, 100)
	l.Okf("checksum", "ok")
	l.Download(5, -1) // a later download starts a new bar
	s := l.Snapshot()
	if got := kinds(s); got != "step,bar,ok,bar" {
		t.Fatalf("kinds %s", got)
	}
	if s.Lines[1].Args["done"] != "100" || s.Lines[1].Text != "100 of 100 bytes (100 %)" {
		t.Errorf("bar %+v", s.Lines[1])
	}
	if s.Lines[3].Text != "5 bytes" || s.Lines[3].Args["total"] != "-1" {
		t.Errorf("bar without size %+v", s.Lines[3])
	}
}

func TestTheLogIsCapped(t *testing.T) {
	var l Log
	l.Start("install", "x")
	for i := 0; i < maxLines+25; i++ {
		l.Output([]byte(fmt.Sprintf("line %d", i)))
	}
	s := l.Snapshot()
	if len(s.Lines) != maxLines || s.Dropped != 25 || s.Lines[0].Text != "line 25" {
		t.Fatalf("%d lines, dropped %d, first %q", len(s.Lines), s.Dropped, s.Lines[0].Text)
	}
}

// Command output arrives in pieces; a line is reported once it is complete.
func TestTheWriterReportsCompleteLinesWhileTheCommandRuns(t *testing.T) {
	var l Log
	l.Start("install", "x")
	w := l.Writer()
	_, _ = w.Write([]byte("Down"))
	if n := len(l.Snapshot().Lines); n != 0 {
		t.Fatalf("half a line reported: %d lines", n)
	}
	_, _ = w.Write([]byte("loading 10%\rDownloading 100%\nMerg"))
	if s := l.Snapshot(); len(s.Lines) != 1 || s.Lines[0].Text != "Downloading 100%" {
		t.Fatalf("after the first newline: %+v", s.Lines)
	}
	_, _ = w.Write([]byte("ing"))
	w.Flush()
	if s := l.Snapshot(); len(s.Lines) != 2 || s.Lines[1].Text != "Merging" {
		t.Fatalf("after flush: %+v", s.Lines)
	}
	if !strings.HasSuffix(string(w.Tail()), "100%\nMerging") {
		t.Errorf("tail %q", w.Tail())
	}
}

func TestANilLogIgnoresEverything(t *testing.T) {
	var l *Log
	l.Start("install", "x")
	l.Stepf("k", "x")
	l.Command("zpkg")
	l.Output([]byte("x"))
	l.Download(1, 2)
	l.Finish(nil)
	if s := l.Snapshot(); s.ID != 0 || s.Lines == nil {
		t.Fatalf("%+v", s)
	}
}
