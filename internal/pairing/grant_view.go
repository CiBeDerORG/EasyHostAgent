package pairing

import "time"

// ActiveGrant 读取当前唯一控制中心的授权与设备身份，不泄漏 Token。
// Task 在提交、读取、取消以及未来真正执行前都必须重新检查此授权。
func (s *Store) ActiveGrant(controllerID, agentID, token string) (string, time.Time, bool) {
	if s == nil || agentID != s.agentID || !validUUID(controllerID) || len(token) != 64 {
		return "", time.Time{}, false
	}
	var deviceID string
	var pairedAt time.Time
	err := s.withLock(func(r *record) (bool, error) {
		if r.Grant != nil && r.Grant.ControllerID == controllerID &&
			secureEqualHex(r.Grant.TokenHash, hash(token)) {
			deviceID = r.Grant.DeviceID
			pairedAt = r.Grant.PairedAt
		}
		return false, nil
	})
	return deviceID, pairedAt, err == nil && deviceID != "" && !pairedAt.IsZero()
}
