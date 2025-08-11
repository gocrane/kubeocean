package utils

import (
	"time"
)

// BoolPtr returns a pointer to the given bool value
func BoolPtr(b bool) *bool {
	return &b
}

// Int64Ptr returns a pointer to the given int64 value
func Int64Ptr(i int64) *int64 {
	return &i
}

// StringPtr returns a pointer to the given string value
func StringPtr(s string) *string {
	return &s
}

// Int32Ptr returns a pointer to the given int32 value
func Int32Ptr(i int32) *int32 {
	return &i
}

// DurationPtr returns a pointer to the given time.Duration value
func DurationPtr(d time.Duration) *time.Duration {
	return &d
}
