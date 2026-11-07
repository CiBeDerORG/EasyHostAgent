package dockerengine

import (
	"context"
	"net/http"
	"testing"
)

func TestReadStatsGroupsRunningAndStopped(t *testing.T) {
	socket := testEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"ApiVersion":"1.41"}`))
		case "/v1.41/containers/json":
			_, _ = w.Write([]byte(`[
				{"Id":"a-web","Names":["/web"],"State":"running","Labels":{"com.docker.compose.project":"test"}},
				{"Id":"b-db","Names":["/db"],"State":"exited","Labels":{"com.docker.compose.project":"test"}}
			]`))
		case "/v1.41/images/json":
			_, _ = w.Write([]byte(`[]`))
		case "/v1.41/containers/a-web/stats":
			if r.URL.Query().Get("stream") != "false" {
				t.Error("CPU/内存采样不能使用长连接流")
			}
			_, _ = w.Write([]byte(`{
				"cpu_stats":{"cpu_usage":{"total_usage":300000000},"system_cpu_usage":2000000000,"online_cpus":4},
				"precpu_stats":{"cpu_usage":{"total_usage":100000000},"system_cpu_usage":1000000000},
				"memory_stats":{"usage":134217728,"limit":536870912}
			}`))
		default:
			t.Errorf("不应访问其他资源：%s", r.URL.String())
			http.NotFound(w, r)
		}
	}))
	client, err := New(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	stats, err := client.ReadStats(context.Background(), "compose:test")
	if err != nil || !stats.Complete || stats.ObservedAt == "" || len(stats.Containers) != 2 {
		t.Fatalf("Compose 资源采样失败：%+v %v", stats, err)
	}
	web, db := stats.Containers[0], stats.Containers[1]
	if !web.Available || web.CPUPercent < 79.9 || web.CPUPercent > 80.1 || web.MemoryBytes != 134217728 || web.MemoryLimitBytes != 536870912 {
		t.Fatalf("CPU/内存计算错误：%+v", web)
	}
	if db.Available || db.State != "exited" {
		t.Fatalf("已停止容器不得被误报为有实时采样：%+v", db)
	}
	if _, err := client.ReadStats(context.Background(), "compose:unknown"); err != ErrResourceNotFound {
		t.Fatalf("不允许读取未知资源：%v", err)
	}
}
