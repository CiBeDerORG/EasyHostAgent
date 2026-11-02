package pairing

import "time"

// IsCurrentGrant 供本机旧任务恢复判断，不接受 Token，不授予任何远程访问。
// 只有当前唯一配对纪元与任务完全相同，才说明该任务仍属于现行控制中心。
func (s *Store) IsCurrentGrant(controllerID, deviceID string, at time.Time) (bool, error) {
	if s == nil || !validUUID(controllerID) || !validUUID(deviceID) || at.IsZero() {
		return false, ErrInvalidIDs
	}
	var active bool
	err := s.withLock(func(record *record) (bool, error) {
		if record.Grant != nil {
			active = record.Grant.ControllerID == controllerID &&
				record.Grant.DeviceID == deviceID &&
				record.Grant.PairedAt.Equal(at)
		}
		return false, nil
	})
	return active, err
}
