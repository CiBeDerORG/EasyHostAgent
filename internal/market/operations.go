package market

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/CiBeDerORG/EasyHostAgent/internal/bundles"
)

func validOperationID(id string) bool {
	part, ok := strings.CutPrefix(id, "AT-")
	return ok && uuid.MatchString(part)
}

func (s *Service) saveOperation(id string, raw []byte) error {
	if !validOperationID(id) {
		return errors.New("无效的更新任务编号")
	}
	dir := filepath.Join(s.records, "operations")
	if err := writeFile(filepath.Join(dir, id+".json"), raw, 0600); err != nil {
		return err
	}
	fd, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer fd.Close()
	return fd.Sync()
}

// 调用方持有服务锁。只将本进程正在执行的同一任务视为 running。
func (s *Service) readOperation(id string) (Record, error) {
	if !validOperationID(id) {
		return Record{}, errors.New("无效的更新任务编号")
	}
	raw, err := os.ReadFile(filepath.Join(s.records, "operations", id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, nil
	}
	var rec Record
	if err != nil || json.Unmarshal(raw, &rec) != nil || rec.OperationID != id ||
		!rec.Ref.valid() || rec.Action != "update" || !bundles.ValidDigest(rec.Digest) {
		return Record{}, errors.New("更新任务记录不可读")
	}
	if rec.Result == "running" && s.activeOperation != id {
		rec.Result = "unknown"
		rec.Message = "上次操作中断，请核对服务状态"
	}
	return rec, nil
}

// OperationStatus 只查询指定应用的精确回执，不需要 Docker 快照。
func (s *Service) OperationStatus(id string, refs []Ref, includeManaged bool) ([]Record, error) {
	if len(refs) != 1 || !refs[0].valid() || includeManaged {
		return nil, errors.New("查询更新任务必须指定唯一应用")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.readOperation(id)
	if err != nil {
		return nil, err
	}
	if rec.OperationID == "" || rec.Ref != refs[0] {
		return []Record{}, nil
	}
	return []Record{rec}, nil
}
