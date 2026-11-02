package tasks

import (
	"time"
)

// RecoverStaleLocal 只允许 Agent 本机可信终端调用，不开放 HTTP。
// 明确确认旧 Controller + 应用 + Task ID，且原配对纪元不再有效时，
// 将遗留的 prepared 转为 superseded，保留审计记录并释放预留。
// 它不运行、撤销或清理任何 Docker 容器、应用目录和数据。
func (s *Store) RecoverStaleLocal(id, expectedController, expectedApp string) (Task, error) {
	if !taskUUID.MatchString(id) || !taskUUID.MatchString(expectedController) ||
		!appID.MatchString(expectedApp) {
		return Task{}, ErrInvalid
	}
	var result Task
	err := s.locked(func() error {
		all, err := s.load()
		if err != nil {
			return err
		}
		for _, task := range all {
			if task.ID != id {
				continue
			}
			if task.ControllerID != expectedController || task.ApplicationID != expectedApp {
				return ErrConflict
			}
			pairedAt, err := time.Parse(time.RFC3339Nano, task.GrantAt)
			if err != nil {
				return ErrCorrupt
			}
			active, err := s.grants.IsCurrentGrant(task.ControllerID, task.DeviceID, pairedAt)
			if err != nil {
				return err
			}
			if active {
				return ErrConflict // 当前有效配对任务应通过已授权 API 取消
			}
			if task.Status == Prepared {
				task.Status = Superseded
				task.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
				if err := s.storeOne(task, true); err != nil {
					return err
				}
			}
			result = task
			return nil
		}
		return ErrNotFound
	})
	return result, err
}
