package dockerengine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testEngine(t *testing.T, handler http.Handler) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	return socket
}

func TestInspectIncludesStoppedContainersAndImages(t *testing.T) {
	seenContainers := false
	seenImages := false
	socket := testEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.String() {
		case "/version":
			_, _ = w.Write([]byte(`{"Version":"28.3.0","ApiVersion":"1.48","MinAPIVersion":"1.41"}`))
		case "/v1.41/containers/json?all=1":
			seenContainers = true
			_, _ = w.Write([]byte(`[
				{"Id":"running","Names":["/web"],"State":"running","Status":"Up 1 hour",
				 "Image":"nginx:stable","Labels":{"com.docker.compose.project":"webstack"},
				 "Ports":[{"PrivatePort":80,"PublicPort":8080,"Type":"tcp"}],
				 "Mounts":[{"Type":"bind","Source":"/srv/data","Destination":"/data","RW":true}],
				 "NetworkSettings":{"Networks":{"stack":{"NetworkID":"abc","IPAddress":"172.18.0.3"}}}},
				{"Id":"stopped","Names":["/db"],"State":"exited","Status":"Exited (0)","Image":"postgres:16"}
			]`))
		case "/v1.41/images/json?all=1":
			seenImages = true
			_, _ = w.Write([]byte(`[{"Id":"sha256:image","RepoTags":["nginx:stable"],"RepoDigests":["nginx@sha256:abc"],"Size":4096}]`))
		default:
			t.Errorf("unexpected Docker request %s", r.URL.String())
			http.NotFound(w, r)
		}
	}))
	client, err := New(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := client.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || got.APIVersion != "1.41" || !seenContainers || !seenImages {
		t.Fatalf("incomplete inventory: %+v", got)
	}
	if len(got.Containers) != 2 || got.Containers[1].State != "exited" {
		t.Fatalf("stopped containers were omitted: %+v", got.Containers)
	}
	web := got.Containers[0]
	if web.Labels["com.docker.compose.project"] != "webstack" ||
		len(web.Ports) != 1 || web.Ports[0].PublicPort != 8080 ||
		len(web.Mounts) != 1 || web.Mounts[0].Destination != "/data" ||
		web.NetworkSettings.Networks["stack"].IPAddress != "172.18.0.3" {
		t.Fatalf("lost Docker metadata: %+v", web)
	}
	if len(got.Images) != 1 || got.Images[0].ID != "sha256:image" {
		t.Fatalf("missing images: %+v", got.Images)
	}
}

func TestIncompleteImageQueryCannotImplyDeletion(t *testing.T) {
	socket := testEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/version":
			_, _ = w.Write([]byte(`{"Version":"27.0","ApiVersion":"1.46","MinAPIVersion":"1.24"}`))
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(`[{"Id":"still-here","State":"exited"}]`))
		case strings.HasSuffix(r.URL.Path, "/images/json"):
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	client, err := New(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	got, err := client.Inspect(context.Background())
	if !errors.Is(err, ErrPermission) || got.Complete || len(got.Containers) != 1 || got.Error == "" {
		t.Fatalf("failed Docker query cannot be treated as a complete scan: %+v, %v", got, err)
	}
}

func TestUnavailableSocketAndVersionNegotiation(t *testing.T) {
	root := t.TempDir()
	client, err := New(filepath.Join(root, "no-docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Inspect(context.Background())
	client.Close()
	if !errors.Is(err, ErrUnavailable) || got.Complete {
		t.Fatalf("missing engine incorrectly returned success: %+v, %v", got, err)
	}
	for _, tt := range []struct {
		max     string
		min     string
		want    string
		invalid bool
	}{
		{max: "1.40", min: "1.24", want: "1.40"},
		{max: "1.48", min: "1.44", want: "1.44"},
		{max: "1.48", min: "", want: "1.41"},
		{max: "1.23", min: "1.20", invalid: true},
		{max: "1.39", min: "1.45", invalid: true},
		{max: "2.1", min: "1.24", invalid: true},
		{max: "garbage", min: "1.24", invalid: true},
	} {
		selected, err := selectAPI(Version{APIVersion: tt.max, MinAPIVersion: tt.min})
		if tt.invalid && err == nil {
			t.Errorf("accepted impossible Docker API version %s/%s", tt.min, tt.max)
		} else if !tt.invalid && (err != nil || selected != tt.want) {
			t.Errorf("negotiate API %s/%s: %s, %v", tt.min, tt.max, selected, err)
		}
	}
	for _, p := range []string{"relative.sock", "/", "/tmp/../tmp/docker.sock"} {
		if _, err := New(p); err == nil {
			t.Errorf("accepted invalid socket path %q", p)
		}
	}
	_ = os.Remove(filepath.Join(root, "no-docker.sock"))
}
