package authn

import "crypto/subtle"

// subtleEqual 常量时间比较两个切片。
func subtleEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}
