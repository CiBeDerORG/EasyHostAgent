package dockerengine

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
)

func makeContainer(id, name, state, project, service string) Container {
	c := Container{ID: id, Names: []string{"/" + name}, State: state}
	c.Labels = map[string]string{}
	if project != "" {
		c.Labels["com.docker.compose.project"] = project
	}
	if service != "" {
		c.Labels["com.docker.compose.service"] = service
	}
	return c
}

func TestGroupSub2APIWithDatabaseAndStandalone(t *testing.T) {
	inventory := Inventory{Complete: true, ObservedAt: "2026-10-28T08:00:00Z", Containers: []Container{
		makeContainer("db-1", "sub2api-db", "exited", "sub2api", "postgres"),
		makeContainer("web-1", "sub2api-web", "running", "sub2api", "api"),
		makeContainer("web-old", "sub2api-old", "exited", "sub2api", "api"),
		makeContainer("proxy", "proxy", "running", "", ""),
		makeContainer("unmanaged-db", "db", "exited", "", "postgres"),
		makeContainer("other", "test-web", "running", "otherstack", "web"),
	}}
	grouped, err := Group(inventory)
	if err != nil || !grouped.Complete || grouped.ObservedAt != inventory.ObservedAt {
		t.Fatalf("must group complete inventory: %+v %v", grouped, err)
	}
	if len(grouped.Resources) != 4 {
		t.Fatalf("got %d groups; want 4: %+v", len(grouped.Resources), grouped.Resources)
	}
	var app *Resource
	for i := range grouped.Resources {
		r := &grouped.Resources[i]
		if r.ID == "compose:sub2api" {
			app = r
		}
		if r.ID == "container:proxy" && (r.Kind != KindContainer || r.ContainerCount != 1) {
			t.Fatalf("standalone proxy improperly merged: %+v", r)
		}
	}
	if app == nil || app.Kind != KindCompose || app.Status != "partial" || app.RunningCount != 1 || app.ContainerCount != 3 || !reflect.DeepEqual(app.Services, []string{"api", "postgres"}) {
		t.Fatalf("wrong Compose group: %+v", app)
	}
	if app.Containers[0].ID != "db-1" || app.Containers[2].ID != "web-old" {
		t.Fatalf("unstable container sort: %+v", app.Containers)
	}
	other, err := Group(Inventory{Complete: true, Containers: []Container{inventory.Containers[5], inventory.Containers[4], inventory.Containers[3], inventory.Containers[2], inventory.Containers[1], inventory.Containers[0]}})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range grouped.Resources {
		if r.ID != other.Resources[i].ID || !reflect.DeepEqual(r.Services, other.Resources[i].Services) {
			t.Fatalf("resource order depends on Docker response order: %+v and %+v", grouped.Resources, other.Resources)
		}
	}
}

func TestGroupIncompleteOrInvalidIsNotAuthoritative(t *testing.T) {
	for _, test := range []struct {
		name     string
		snapshot Inventory
		want     error
	}{
		{"engine_failed", Inventory{Complete: false, Containers: []Container{makeContainer("known", "known", "running", "app", "web")}}, ErrIncomplete},
		{"empty_id", Inventory{Complete: true, Containers: []Container{makeContainer("", "bad", "running", "app", "web")}}, ErrProtocol},
		{"duplicate_id", Inventory{Complete: true, Containers: []Container{makeContainer("same", "a", "running", "a", "one"), makeContainer("same", "b", "running", "b", "two")}}, ErrProtocol},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Group(test.snapshot)
			if !errors.Is(err, test.want) || got.Complete || len(got.Resources) != 0 {
				t.Fatalf("unsafe grouping %+v %v", got, err)
			}
		})
	}
}

func TestGroupEmptyAndStatuses(t *testing.T) {
	empty, err := Group(Inventory{Complete: true})
	if err != nil || !empty.Complete || len(empty.Resources) != 0 || empty.Resources == nil {
		t.Fatalf("empty Engine must yield an authoritative empty list: %+v %v", empty, err)
	}
	for _, tc := range []struct {
		states []string
		want   string
	}{
		{[]string{"running", "running"}, "running"},
		{[]string{"exited", "created", "dead"}, "stopped"},
		{[]string{"running", "exited"}, "partial"},
		{[]string{"paused", "exited"}, "unknown"},
	} {
		var cc []Container
		for i, state := range tc.states {
			cc = append(cc, makeContainer(string(rune('a'+i)), "n", state, "one", "service"))
		}
		got, err := Group(Inventory{Complete: true, Containers: cc})
		if err != nil || len(got.Resources) != 1 || got.Resources[0].Status != tc.want {
			t.Fatalf("states %v status %+v, %v", tc.states, got.Resources, err)
		}
	}
}

func TestInspectGroupedOverEngineSocket(t *testing.T) {
	socket := testEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.String() {
		case "/version":
			_, _ = w.Write([]byte(`{"Version":"28.0","ApiVersion":"1.48","MinAPIVersion":"1.41"}`))
		case "/v1.41/containers/json?all=1":
			_, _ = w.Write([]byte(`[
				{"Id":"web","Names":["/web"],"State":"running","Labels":{"com.docker.compose.project":"sub2api","com.docker.compose.service":"api"}},
				{"Id":"db","Names":["/db"],"State":"exited","Labels":{"com.docker.compose.project":"sub2api","com.docker.compose.service":"postgres"}}
			]`))
		case "/v1.41/images/json?all=1":
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
	grouped, err := client.InspectGrouped(context.Background())
	if err != nil || !grouped.Complete || len(grouped.Resources) != 1 ||
		grouped.Resources[0].ID != "compose:sub2api" || grouped.Resources[0].Status != "partial" {
		t.Fatalf("invalid Engine-to-Compose integration: %+v, %v", grouped, err)
	}

	missing, err := New(filepath.Join(t.TempDir(), "missing.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Close()
	invalid, err := missing.InspectGrouped(context.Background())
	if !errors.Is(err, ErrIncomplete) || !errors.Is(err, ErrUnavailable) || invalid.Complete || len(invalid.Resources) != 0 {
		t.Fatalf("missing engine produced authoritative data: %+v, %v", invalid, err)
	}
}
