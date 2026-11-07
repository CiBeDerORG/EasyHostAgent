package dockerengine

import (
	"context"
	"sync"
	"time"
)

// ContainerStats 是一次非流式采样。未运行的容器不访问 Engine stats 接口。
type ContainerStats struct {
	ContainerID      string  `json:"container_id"`
	State            string  `json:"state"`
	Available        bool    `json:"available"`
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryBytes      uint64  `json:"memory_bytes"`
	MemoryLimitBytes uint64  `json:"memory_limit_bytes"`
}

// ResourceStats 表示单次完整采样；任何运行中容器失败均返回错误，不输出部分结果。
type ResourceStats struct {
	ResourceID string           `json:"resource_id"`
	ObservedAt string           `json:"observed_at"`
	Complete   bool             `json:"complete"`
	Containers []ContainerStats `json:"containers"`
}

type engineStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PercpuUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  uint64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"`
		Limit uint64 `json:"limit"`
	} `json:"memory_stats"`
}

func cpuPercent(s engineStats) float64 {
	current, previous := s.CPUStats, s.PreCPUStats
	if current.CPUUsage.TotalUsage < previous.CPUUsage.TotalUsage || current.SystemUsage <= previous.SystemUsage {
		return 0
	}
	cpuDelta := current.CPUUsage.TotalUsage - previous.CPUUsage.TotalUsage
	systemDelta := current.SystemUsage - previous.SystemUsage
	cores := current.OnlineCPUs
	if cores == 0 {
		cores = uint64(len(current.CPUUsage.PercpuUsage))
	}
	if cores == 0 {
		cores = 1
	}
	return float64(cpuDelta) / float64(systemDelta) * float64(cores) * 100
}

func (c *Client) singleStats(ctx context.Context, api string, item Container) (ContainerStats, error) {
	result := ContainerStats{ContainerID: item.ID, State: item.State}
	if item.State != "running" {
		return result, nil
	}
	if !validContainerID(item.ID) {
		return result, ErrProtocol
	}
	var raw engineStats
	if err := c.read(ctx, "/v"+api+"/containers/"+item.ID+"/stats?stream=false", &raw); err != nil {
		return result, err
	}
	result.Available = true
	result.CPUPercent = cpuPercent(raw)
	result.MemoryBytes = raw.MemoryStats.Usage
	result.MemoryLimitBytes = raw.MemoryStats.Limit
	return result, nil
}

// ReadStats 仅查询完整清单中明确存在的资源，最多并行读取四个容器，
// 并限定单次采集的容器数。远程 Docker 主机地址不受调用者控制。
func (c *Client) ReadStats(ctx context.Context, resourceID string) (ResourceStats, error) {
	result := ResourceStats{ResourceID: resourceID, Containers: []ContainerStats{}}
	resource, api, err := c.findResource(ctx, resourceID)
	if err != nil {
		return result, err
	}
	if len(resource.Containers) > 32 {
		return result, ErrProtocol
	}
	items := make([]ContainerStats, len(resource.Containers))
	sem := make(chan struct{}, 4)
	failures := make(chan error, len(items))
	var workers sync.WaitGroup
	for i, item := range resource.Containers {
		workers.Add(1)
		go func(index int, container Container) {
			defer workers.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				failures <- ctx.Err()
				return
			}
			value, readErr := c.singleStats(ctx, api, container)
			if readErr != nil {
				failures <- readErr
				return
			}
			items[index] = value
		}(i, item)
	}
	workers.Wait()
	close(failures)
	for failure := range failures {
		if failure != nil {
			return result, failure
		}
	}
	result.ObservedAt = time.Now().UTC().Format(time.RFC3339)
	result.Containers = items
	result.Complete = true
	return result, nil
}
