package dockerengine

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"
)

func TestActComposeTargetsEveryMemberAndVerifies(t *testing.T) {
	var mu sync.Mutex
	states := map[string]string{"a-web": "running", "b-db": "running"}
	var actions []string
	socket := testEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"ApiVersion":"1.41","MinAPIVersion":"1.24"}`))
		case "/v1.41/containers/json":
			_, _ = w.Write([]byte(`[
				{"Id":"a-web","Names":["/web"],"State":"` + states["a-web"] + `","Labels":{"com.docker.compose.project":"sub2api","com.docker.compose.service":"web"}},
				{"Id":"b-db","Names":["/db"],"State":"` + states["b-db"] + `","Labels":{"com.docker.compose.project":"sub2api","com.docker.compose.service":"db"}}
			]`))
		case "/v1.41/images/json":
			_, _ = w.Write([]byte(`[]`))
		case "/v1.41/containers/a-web/stop", "/v1.41/containers/b-db/stop":
			if r.Method != http.MethodPost {
				t.Error("Docker mutation used wrong method")
			}
			id := "a-web"
			if r.URL.Path == "/v1.41/containers/b-db/stop" {
				id = "b-db"
			}
			actions = append(actions, id)
			states[id] = "exited"
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected engine path %s", r.URL.String())
			http.NotFound(w, r)
		}
	}))
	client, err := New(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Act(context.Background(), "compose:sub2api", "stop")
	if err != nil || !result.Verified || result.FinalStatus != "stopped" ||
		!reflect.DeepEqual(result.Attempted, []string{"a-web", "b-db"}) ||
		!reflect.DeepEqual(result.Succeeded, []string{"a-web", "b-db"}) {
		t.Fatalf("entire Compose project was not stopped: %+v %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(actions, []string{"a-web", "b-db"}) {
		t.Fatalf("unexpected mutation order: %v", actions)
	}
}

func TestActRejectsUnknownResourceAndPartialFailure(t *testing.T) {
	var mu sync.Mutex
	var attempted []string
	socket := testEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"ApiVersion":"1.41"}`))
		case "/v1.41/containers/json":
			_, _ = w.Write([]byte(`[
				{"Id":"a-web","State":"running","Labels":{"com.docker.compose.project":"app"}},
				{"Id":"b-db","State":"running","Labels":{"com.docker.compose.project":"app"}}
			]`))
		case "/v1.41/images/json":
			_, _ = w.Write([]byte(`[]`))
		case "/v1.41/containers/a-web/stop":
			attempted = append(attempted, "a-web")
			w.WriteHeader(http.StatusNoContent)
		case "/v1.41/containers/b-db/stop":
			attempted = append(attempted, "b-db")
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	c, err := New(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Act(context.Background(), "compose:nonexistent", "stop"); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("unknown resource permitted: %v", err)
	}
	if _, err = c.Act(context.Background(), "compose:app", "rm"); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("arbitrary action permitted: %v", err)
	}
	result, err := c.Act(context.Background(), "compose:app", "stop")
	if !errors.Is(err, ErrPartialOperation) || result.Verified || !reflect.DeepEqual(result.Succeeded, []string{"a-web"}) {
		t.Fatalf("partial Docker failure misreported as success: %+v %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(attempted, []string{"a-web", "b-db"}) {
		t.Fatalf("Compose operation did not cover both members: %v", attempted)
	}
}
