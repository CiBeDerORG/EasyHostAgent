package market

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
	"github.com/CiBeDerORG/EasyHostAgent/internal/config"
	"github.com/CiBeDerORG/EasyHostAgent/internal/dockerengine"
)

func macOSEasyNetTopoFixture(t *testing.T) (*Service, Request, *dockerengine.Resource, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "software")
	data := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	cache, err := bundles.New(data)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "easynettopo", "deploy")
	deploy := filepath.Join(root, "deploy")
	for _, dir := range []string{work, filepath.Join(root, "easynettopo", "data"), filepath.Join(deploy, "macos")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	put := func(file string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(file, []byte("echo fixture\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(deploy, "deploy.sh"), 0755)
	put(filepath.Join(deploy, "macos", "deploy.sh"), 0755)
	put(filepath.Join(deploy, "macos", "compose-install.sh"), 0755)
	put(filepath.Join(work, "docker-compose.yaml"), 0644)
	put(filepath.Join(work, "docker-compose.macos.yaml"), 0644)
	put(filepath.Join(root, "easynettopo", "data", "app.db"), 0600)
	put(filepath.Join(deploy, ".env"), 0600)
	put(filepath.Join(deploy, ".secrets"), 0600)
	// 只有经过身份校验才允许调用这个原有固定入口；永远不执行包内 update.sh。
	err = os.WriteFile(filepath.Join(deploy, "deploy.sh"),
		[]byte("test \"$1\" = easynettopo && test \"$2\" = update && printf ok > \"$SOFTWARE_ROOT/updated\"\n"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	project := &dockerengine.Resource{
		ComposeProject: "easynettopo", Status: "running", Services: []string{"easynettopo"},
		Containers: []dockerengine.Container{{Labels: map[string]string{
			"com.docker.compose.project":              "easynettopo",
			"com.docker.compose.service":              "easynettopo",
			"com.docker.compose.project.working_dir":  work,
			"com.docker.compose.project.config_files": filepath.Join(work, "docker-compose.yaml") + "," + filepath.Join(work, "docker-compose.macos.yaml"),
		}}},
	}
	s, err := New(context.Background(), config.Config{SoftwareRoot: root, DataDir: data, Platform: "macos"}, "",
		cache, func(context.Context) (dockerengine.GroupedInventory, error) {
			return dockerengine.GroupedInventory{Complete: true, Resources: []dockerengine.Resource{*project}}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Ref: Ref{Source: testSource, App: "easynettopo"}, Digest: legacyBundle(t, cache, "easynettopo", 2)}
	return s, req, project, root
}

func TestMacOSEasyNetTopoHookReusesVerifiedDeploy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("macOS Deploy must run as an unprivileged user")
	}
	s, req, _, root := macOSEasyNetTopoFixture(t)
	ctx := context.Background()
	rows, err := s.Status(ctx, testController, []Ref{req.Ref}, false)
	if err != nil || len(rows) != 1 || !rows[0].Managed || rows[0].External ||
		!rows[0].Allowed || rows[0].Mode != macOSEasyNetTopoMode {
		t.Fatalf("valid native macOS deployment should be updateable: %+v, %v", rows, err)
	}
	if _, err := s.Start(ctx, testController, "install", req, func() bool { return true }); err == nil {
		t.Fatal("native macOS EasyNetTopo must not be installed from the app market")
	}
	if _, err := s.Start(ctx, testController, "uninstall", Request{Ref: req.Ref}, func() bool { return true }); err == nil {
		t.Fatal("native macOS EasyNetTopo must not be uninstalled from the app market")
	}
	if _, err := s.Start(ctx, "invalid", "update", req, func() bool { return true }); !errors.Is(err, ErrDenied) {
		t.Fatalf("unpaired update must be denied: %v", err)
	}
	if _, err := s.Start(ctx, testController, "update", req, func() bool { return true }); err != nil {
		t.Fatalf("verified installed macOS app should update without prior Agent record: %v", err)
	}
	s.Wait()
	record, err := s.read("easynettopo")
	if err != nil || !record.Managed || record.Mode != macOSEasyNetTopoMode ||
		record.Action != "update" || record.Result != "succeeded" || record.Digest != req.Digest {
		t.Fatalf("unexpected update receipt: %+v, %v", record, err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "updated")); err != nil || string(raw) != "ok" {
		t.Fatalf("did not call the existing deploy CLI: %q, %v", raw, err)
	}
	if _, err := s.Start(ctx, testController, "update", req, func() bool { return true }); err != nil {
		t.Fatalf("a second update should reuse the managed record: %v", err)
	}
	s.Wait()
}

func TestMacOSEasyNetTopoRefusesExternalOrUnsafeDeployment(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("macOS Deploy must run as an unprivileged user")
	}
	for _, tc := range []struct {
		name   string
		tamper func(*testing.T, *dockerengine.Resource, string)
	}{
		{"wrong compose identity", func(t *testing.T, p *dockerengine.Resource, _ string) {
			p.Containers[0].Labels["com.docker.compose.project.config_files"] = "/external/docker-compose.yaml"
		}},
		{"wrong service", func(t *testing.T, p *dockerengine.Resource, _ string) {
			p.Containers[0].Labels["com.docker.compose.service"] = "other"
		}},
		{"missing database", func(t *testing.T, _ *dockerengine.Resource, root string) {
			if err := os.Remove(filepath.Join(root, "easynettopo", "data", "app.db")); err != nil {
				t.Fatal(err)
			}
		}},
		{"world-writable script", func(t *testing.T, _ *dockerengine.Resource, root string) {
			if err := os.Chmod(filepath.Join(root, "deploy", "deploy.sh"), 0777); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlinked script", func(t *testing.T, _ *dockerengine.Resource, root string) {
			file := filepath.Join(root, "deploy", "deploy.sh")
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "other-script.sh"), file); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, req, project, root := macOSEasyNetTopoFixture(t)
			tc.tamper(t, project, root)
			rows, err := s.Status(context.Background(), testController, []Ref{req.Ref}, false)
			if err != nil || len(rows) != 1 || rows[0].Managed && !rows[0].External {
				t.Fatalf("external deployment must not appear managed: %+v, %v", rows, err)
			}
			if _, err := s.Start(context.Background(), testController, "update", req, func() bool { return true }); err == nil {
				t.Fatal("unverified local update was accepted")
			}
			if _, err := os.Stat(filepath.Join(root, "updated")); !os.IsNotExist(err) {
				t.Fatal("untrusted deployment executed the update script")
			}
		})
	}
}
