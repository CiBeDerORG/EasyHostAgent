package dockerengine

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func dockerFrame(stream byte, body string) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:8], uint32(len(body)))
	return append(header, []byte(body)...)
}

func TestReadLogsDemultiplexesAndBoundsOutput(t *testing.T) {
	var logTail string
	socket := testEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"ApiVersion":"1.41"}`))
		case "/v1.41/containers/json":
			_, _ = w.Write([]byte(`[{"Id":"web","Names":["/web"],"State":"running","Labels":{}},{"Id":"db","Names":["/db"],"State":"exited","Labels":{}}]`))
		case "/v1.41/images/json":
			_, _ = w.Write([]byte(`[]`))
		case "/v1.41/containers/web/logs":
			logTail = r.URL.Query().Get("tail")
			if r.URL.Query().Get("follow") != "0" {
				t.Error("Docker log request attempted to follow indefinitely")
			}
			_, _ = w.Write(dockerFrame(1, "web: ok\n"))
			_, _ = w.Write(dockerFrame(2, "warning\n"))
		case "/v1.41/containers/db/logs":
			_, _ = w.Write([]byte(strings.Repeat("a", MaxLogBytes+20)))
		default:
			http.NotFound(w, r)
		}
	}))
	client, err := New(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	logs, err := client.ReadLogs(context.Background(), "container:web", 37)
	if err != nil || logs.Truncated || logTail != "37" || !strings.Contains(logs.Text, "web: ok\nwarning\n") {
		t.Fatalf("Docker stream framing incorrectly parsed: %+v %v", logs, err)
	}
	logs, err = client.ReadLogs(context.Background(), "container:db", 10)
	if err != nil || !logs.Truncated || len(logs.Text) > MaxLogBytes {
		t.Fatalf("large log was not bounded: len=%d %+v %v", len(logs.Text), logs, err)
	}
	if _, err = client.ReadLogs(context.Background(), "container:web", 501); !errors.Is(err, ErrProtocol) {
		t.Fatalf("unbounded tail accepted: %v", err)
	}
	if _, err = client.ReadLogs(context.Background(), "container:../evil", 100); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("unrecognized path injection accepted: %v", err)
	}
}

func TestDockerRawLogFrames(t *testing.T) {
	if got := parseDockerLog([]byte("plain tty log\n")); got != "plain tty log\n" {
		t.Fatalf("raw TTY corrupted: %q", got)
	}
	if got := parseDockerLog(append(dockerFrame(1, "hello"), dockerFrame(2, " world")...)); got != "hello world" {
		t.Fatalf("stream frames corrupted: %q", got)
	}
}
