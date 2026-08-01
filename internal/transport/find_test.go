package transport

import (
	"bytes"
	"io"
	"reflect"
	"testing"
	"time"
)

func TestParseFindRecord_File(t *testing.T) {
	fi, err := parseFindRecord([]byte("F\t1234\t1767225600.123456789\t/tmp/x/a.txt"))
	if err != nil {
		t.Fatalf("parseFindRecord: %v", err)
	}
	want := FileInfo{Path: "/tmp/x/a.txt", Size: 1234, ModTime: time.Unix(1767225600, 0)}
	if fi.Path != want.Path || fi.Size != want.Size || !fi.ModTime.Equal(want.ModTime) || fi.IsDir {
		t.Fatalf("got %+v want %+v", fi, want)
	}
}

func TestParseFindRecord_Dir(t *testing.T) {
	fi, err := parseFindRecord([]byte("D\t/tmp/x/sub"))
	if err != nil {
		t.Fatalf("parseFindRecord: %v", err)
	}
	if !fi.IsDir || fi.Path != "/tmp/x/sub" {
		t.Fatalf("got %+v want dir /tmp/x/sub", fi)
	}
}

func TestParseFindRecord_PathWithSpace(t *testing.T) {
	fi, err := parseFindRecord([]byte("F\t10\t100.0\t/tmp/x/my file.txt"))
	if err != nil {
		t.Fatalf("parseFindRecord: %v", err)
	}
	if fi.Path != "/tmp/x/my file.txt" {
		t.Fatalf("path = %q", fi.Path)
	}
}

func TestParseFindRecord_PathWithTab(t *testing.T) {
	// The path is the last field and may itself contain tabs.
	fi, err := parseFindRecord([]byte("F\t10\t100.0\t/tmp/x/a\tb.txt"))
	if err != nil {
		t.Fatalf("parseFindRecord: %v", err)
	}
	if fi.Path != "/tmp/x/a\tb.txt" {
		t.Fatalf("path = %q", fi.Path)
	}
}

func TestParseFindRecord_Malformed(t *testing.T) {
	for _, rec := range [][]byte{
		[]byte("F\t10"),         // too few fields
		[]byte("F\tx\t1.0\t/p"), // bad size
		[]byte("F\t1\tabc\t/p"), // bad mtime
		[]byte("X\twhatever"),   // unknown type
		[]byte("D"),             // dir without path
		{},
	} {
		if _, err := parseFindRecord(rec); err == nil {
			t.Errorf("expected error for %q", rec)
		}
	}
}

func TestParseFindStream_Multiple(t *testing.T) {
	data := "F\t1\t100.0\t/tmp/a\000D\t/tmp/dir\000F\t2\t200.0\t/tmp/b\000"
	var got []FileInfo
	if err := parseFindStream(bytes.NewReader([]byte(data)), func(fi FileInfo) {
		got = append(got, fi)
	}); err != nil {
		t.Fatalf("parseFindStream: %v", err)
	}
	want := []FileInfo{
		{Path: "/tmp/a", Size: 1, ModTime: time.Unix(100, 0)},
		{Path: "/tmp/dir", IsDir: true},
		{Path: "/tmp/b", Size: 2, ModTime: time.Unix(200, 0)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestParseFindStream_Empty(t *testing.T) {
	var got []FileInfo
	if err := parseFindStream(bytes.NewReader(nil), func(fi FileInfo) {
		got = append(got, fi)
	}); err != nil {
		t.Fatalf("parseFindStream: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(got))
	}
}

func TestParseFindStream_NoTrailingNUL(t *testing.T) {
	// A record without a trailing NUL (stream ended) must still be parsed.
	data := "F\t7\t7.0\t/tmp/z"
	var got []FileInfo
	if err := parseFindStream(bytes.NewReader([]byte(data)), func(fi FileInfo) {
		got = append(got, fi)
	}); err != nil {
		t.Fatalf("parseFindStream: %v", err)
	}
	if len(got) != 1 || got[0].Path != "/tmp/z" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseFindStream_Error(t *testing.T) {
	// Malformed record in the middle of the stream must surface as an error.
	data := "F\t1\t1.0\t/ok\000F\tbad\000F\t2\t2.0\t/ok2\000"
	err := parseFindStream(bytes.NewReader([]byte(data)), func(FileInfo) {})
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("plain"); got != "'plain'" {
		t.Fatalf("got %q", got)
	}
	if got := shellQuote("a'b"); got != "'a'\\''b'" {
		t.Fatalf("got %q", got)
	}
	if got := shellQuote("/tmp/with space"); got != "'/tmp/with space'" {
		t.Fatalf("got %q", got)
	}
}

var _ io.Reader = (*bytes.Reader)(nil) // keep io import used if helpers change
