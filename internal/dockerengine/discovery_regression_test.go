package dockerengine

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestT0207MultiProjectStoppedStandaloneAndRepeatedDiscovery(t *testing.T) {
	var requests atomic.Int64
	// Deliberately shuffle the Engine response on every refresh. Resource IDs,
	// Compose grouping and service ordering must remain deterministic.
	containerResponses := []string{
		`[
			{"Id":"redis-1","Names":["/sub2api-redis"],"Image":"redis:7","State":"running","Labels":{"com.docker.compose.project":"sub2api","com.docker.compose.service":"redis"}},
			{"Id":"portainer-1","Names":["/portainer"],"Image":"portainer:latest","State":"exited","Labels":{"com.docker.compose.project":"portainer","com.docker.compose.service":"portainer"}},
			{"Id":"standalone-a","Names":["/standalone"],"Image":"nginx:stable","State":"running","Labels":{}},
			{"Id":"sub2api-1","Names":["/sub2api"],"Image":"sub2api:latest","State":"running","Labels":{"com.docker.compose.project":"sub2api","com.docker.compose.service":"api"}},
			{"Id":"standalone-b","Names":["/redis"],"Image":"redis:7","State":"exited","Labels":{}},
			{"Id":"pg-1","Names":["/sub2api-db"],"Image":"postgres:16","State":"exited","Labels":{"com.docker.compose.project":"sub2api","com.docker.compose.service":"postgres"}}
		]`,
		`[
			{"Id":"pg-1","Names":["/sub2api-db"],"Image":"postgres:16","State":"exited","Labels":{"com.docker.compose.project":"sub2api","com.docker.compose.service":"postgres"}},
			{"Id":"standalone-b","Names":["/redis"],"Image":"redis:7","State":"exited","Labels":{}},
			{"Id":"sub2api-1","Names":["/sub2api"],"Image":"sub2api:latest","State":"running","Labels":{"com.docker.compose.project":"sub2api","com.docker.compose.service":"api"}},
			{"Id":"standalone-a","Names":["/standalone"],"Image":"nginx:stable","State":"running","Labels":{}},
			{"Id":"portainer-1","Names":["/portainer"],"Image":"portainer:latest","State":"exited","Labels":{"com.docker.compose.project":"portainer","com.docker.compose.service":"portainer"}},
			{"Id":"redis-1","Names":["/sub2api-redis"],"Image":"redis:7","State":"running","Labels":{"com.docker.compose.project":"sub2api","com.docker.compose.service":"redis"}}
		]`,
	}
	socket := testEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"Version":"28.0","ApiVersion":"1.48","MinAPIVersion":"1.41"}`))
		case "/v1.41/containers/json":
			if r.URL.Query().Get("all") != "1" {
				t.Error("stopped containers excluded from request")
			}
			n := int(requests.Add(1) - 1)
			_, _ = w.Write([]byte(containerResponses[n%len(containerResponses)]))
		case "/v1.41/images/json":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	client, err := New(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	previousIDs := []string{}
	for iteration := 0; iteration < 5; iteration++ {
		grouped, err := client.InspectGrouped(context.Background())
		if err != nil || !grouped.Complete {
			t.Fatalf("scan %d incomplete: %+v %v", iteration, grouped, err)
		}
		ids := []string{}
		for _, resource := range grouped.Resources {
			ids = append(ids, resource.ID)
			switch resource.ID {
			case "compose:portainer":
				if resource.Kind != KindCompose || resource.ContainerCount != 1 || resource.Status != "stopped" {
					t.Fatalf("stopped Compose disappeared: %+v", resource)
				}
			case "compose:sub2api":
				if resource.ContainerCount != 3 || resource.RunningCount != 2 ||
					resource.Status != "partial" ||
					!reflect.DeepEqual(resource.Services, []string{"api", "postgres", "redis"}) {
					t.Fatalf("multi-service Compose was split or miscounted: %+v", resource)
				}
			case "container:standalone-a", "container:standalone-b":
				if resource.Kind != KindContainer || resource.ContainerCount != 1 {
					t.Fatalf("standalone falsely merged: %+v", resource)
				}
			default:
				t.Fatalf("unexpected group: %+v", resource)
			}
		}
		if len(ids) != 4 {
			t.Fatalf("repeated scan generated duplicate groups: %v", ids)
		}
		if iteration != 0 && !reflect.DeepEqual(ids, previousIDs) {
			t.Fatalf("scan %d changed stable group identity: %v versus %v", iteration, ids, previousIDs)
		}
		previousIDs = ids
	}
	if requests.Load() != 5 {
		t.Fatalf("unexpected Docker polling or retry: %d", requests.Load())
	}
}

func TestT0207EnginePartialFailureCannotDeleteLastKnownResources(t *testing.T) {
	var imageFailed atomic.Bool
	socket := testEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"ApiVersion":"1.41"}`))
		case "/v1.41/containers/json":
			_, _ = w.Write([]byte(`[{"Id":"stopped","State":"exited","Labels":{"com.docker.compose.project":"portainer"}}]`))
		case "/v1.41/images/json":
			if imageFailed.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	c, err := New(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first, err := c.InspectGrouped(context.Background())
	if err != nil || !first.Complete || len(first.Resources) != 1 {
		t.Fatalf("baseline should include exited container: %+v %v", first, err)
	}
	imageFailed.Store(true)
	failed, err := c.InspectGrouped(context.Background())
	if !errors.Is(err, ErrIncomplete) || failed.Complete || len(failed.Resources) != 0 {
		t.Fatalf("failed image listing created deletion evidence: %+v %v", failed, err)
	}
	imageFailed.Store(false)
	again, err := c.InspectGrouped(context.Background())
	if err != nil || !again.Complete || !reflect.DeepEqual(first.Resources, again.Resources) {
		t.Fatalf("Engine recovery changed resource identity: %+v %v", again, err)
	}
}

func TestT0207TwoDockerHostsDoNotMergeSameComposeProject(t *testing.T) {
	makeHost := func(id string) *Client {
		t.Helper()
		socket := testEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/version":
				_, _ = w.Write([]byte(`{"ApiVersion":"1.41"}`))
			case "/v1.41/containers/json":
				_, _ = w.Write([]byte(`[{"Id":"` + id + `","State":"running","Labels":{"com.docker.compose.project":"shared"}}]`))
			case "/v1.41/images/json":
				_, _ = w.Write([]byte(`[]`))
			default:
				http.NotFound(w, r)
			}
		}))
		c, err := New(socket)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		return c
	}
	first, err := makeHost("host-a").InspectGrouped(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := makeHost("host-b").InspectGrouped(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Resources[0].ID != second.Resources[0].ID ||
		first.Resources[0].Containers[0].ID == second.Resources[0].Containers[0].ID {
		t.Fatalf("host identity must scope shared Compose labels: %+v %+v", first, second)
	}
}
