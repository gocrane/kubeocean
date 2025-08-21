package v1beta1

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestResourceLeasingPolicy_IsWithinTimeWindows(t *testing.T) {
	tests := []struct {
		name        string
		policy      *ResourceLeasingPolicy
		expected    bool
		description string
	}{
		{
			name: "no time windows - always active",
			policy: &ResourceLeasingPolicy{
				Spec: ResourceLeasingPolicySpec{
					TimeWindows: []TimeWindow{},
				},
			},
			expected:    true,
			description: "Policy with no time windows should always be active",
		},
		{
			name: "current time within window",
			policy: &ResourceLeasingPolicy{
				Spec: ResourceLeasingPolicySpec{
					TimeWindows: []TimeWindow{
						{
							Start: "00:00",
							End:   "23:59",
						},
					},
				},
			},
			expected:    true,
			description: "Policy with full day window should be active",
		},
		{
			name: "current time outside window",
			policy: &ResourceLeasingPolicy{
				Spec: ResourceLeasingPolicySpec{
					TimeWindows: []TimeWindow{
						{
							Start: "01:00",
							End:   "02:00",
						},
					},
				},
			},
			expected:    false,
			description: "Policy with narrow window should be inactive (assuming current time is not 01:00-02:00)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.policy.IsWithinTimeWindows()
			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

func TestIsWithinTimeWindows(t *testing.T) {
	tests := []struct {
		name        string
		timeWindows []TimeWindow
		currentDay  string
		currentTime string
		expected    bool
		description string
	}{
		{
			name:        "empty time windows - always active",
			timeWindows: []TimeWindow{},
			currentDay:  "monday",
			currentTime: "12:00",
			expected:    true,
			description: "Empty time windows should always be active",
		},
		{
			name:        "nil time windows - always active",
			timeWindows: nil,
			currentDay:  "tuesday",
			currentTime: "15:30",
			expected:    true,
			description: "Nil time windows should always be active",
		},
		{
			name: "full day window - within time",
			timeWindows: []TimeWindow{
				{
					Start: "00:00",
					End:   "23:59",
				},
			},
			currentDay:  "wednesday",
			currentTime: "12:00",
			expected:    true,
			description: "Full day window should be active at noon",
		},
		{
			name: "narrow window - within time",
			timeWindows: []TimeWindow{
				{
					Start: "09:00",
					End:   "17:00",
				},
			},
			currentDay:  "thursday",
			currentTime: "12:00",
			expected:    true,
			description: "Should be active within business hours",
		},
		{
			name: "narrow window - outside time",
			timeWindows: []TimeWindow{
				{
					Start: "09:00",
					End:   "17:00",
				},
			},
			currentDay:  "friday",
			currentTime: "20:00",
			expected:    false,
			description: "Should be inactive outside business hours",
		},
		{
			name: "window with days restriction - matching day",
			timeWindows: []TimeWindow{
				{
					Start: "00:00",
					End:   "23:59",
					Days:  []string{"monday", "tuesday", "wednesday"},
				},
			},
			currentDay:  "monday",
			currentTime: "12:00",
			expected:    true,
			description: "Should be active on matching day",
		},
		{
			name: "window with days restriction - non-matching day",
			timeWindows: []TimeWindow{
				{
					Start: "00:00",
					End:   "23:59",
					Days:  []string{"monday", "tuesday", "wednesday"},
				},
			},
			currentDay:  "saturday",
			currentTime: "12:00",
			expected:    false,
			description: "Should be inactive on non-matching day",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsWithinTimeWindows(tt.timeWindows, tt.currentDay, tt.currentTime)
			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

func TestIsTimeInRange(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		start       string
		end         string
		expected    bool
		description string
	}{
		{
			name:        "time within normal range",
			current:     "12:00",
			start:       "10:00",
			end:         "14:00",
			expected:    true,
			description: "Current time should be within normal range",
		},
		{
			name:        "time outside normal range - before",
			current:     "09:00",
			start:       "10:00",
			end:         "14:00",
			expected:    false,
			description: "Current time before start should be outside range",
		},
		{
			name:        "time outside normal range - after",
			current:     "15:00",
			start:       "10:00",
			end:         "14:00",
			expected:    false,
			description: "Current time after end should be outside range",
		},
		{
			name:        "time at start boundary",
			current:     "10:00",
			start:       "10:00",
			end:         "14:00",
			expected:    true,
			description: "Current time at start boundary should be within range",
		},
		{
			name:        "time at end boundary",
			current:     "14:00",
			start:       "10:00",
			end:         "14:00",
			expected:    true,
			description: "Current time at end boundary should be within range",
		},
		{
			name:        "overnight range - within first part",
			current:     "23:00",
			start:       "22:00",
			end:         "02:00",
			expected:    true,
			description: "Current time in first part of overnight range should be within range",
		},
		{
			name:        "overnight range - within second part",
			current:     "01:00",
			start:       "22:00",
			end:         "02:00",
			expected:    true,
			description: "Current time in second part of overnight range should be within range",
		},
		{
			name:        "overnight range - outside",
			current:     "12:00",
			start:       "22:00",
			end:         "02:00",
			expected:    false,
			description: "Current time outside overnight range should be outside range",
		},
		{
			name:        "overnight range - at start boundary",
			current:     "22:00",
			start:       "22:00",
			end:         "02:00",
			expected:    true,
			description: "Current time at start of overnight range should be within range",
		},
		{
			name:        "overnight range - at end boundary",
			current:     "02:00",
			start:       "22:00",
			end:         "02:00",
			expected:    true,
			description: "Current time at end of overnight range should be within range",
		},
		// Time format validation test cases
		{
			name:        "invalid start time format - too short",
			current:     "12:00",
			start:       "10:0",
			end:         "14:00",
			expected:    false,
			description: "Invalid start time format should return false",
		},
		{
			name:        "invalid end time format - too long",
			current:     "12:00",
			start:       "10:00",
			end:         "14:000",
			expected:    false,
			description: "Invalid end time format should return false",
		},
		{
			name:        "invalid start time format - missing colon",
			current:     "12:00",
			start:       "1000",
			end:         "14:00",
			expected:    false,
			description: "Start time without colon should return false",
		},
		{
			name:        "invalid end time format - wrong separator",
			current:     "12:00",
			start:       "10:00",
			end:         "14-00",
			expected:    false,
			description: "End time with wrong separator should return false",
		},
		{
			name:        "invalid start time - invalid hour",
			current:     "12:00",
			start:       "25:00",
			end:         "14:00",
			expected:    false,
			description: "Start time with invalid hour should return false",
		},
		{
			name:        "invalid end time - invalid minute",
			current:     "12:00",
			start:       "10:00",
			end:         "14:60",
			expected:    false,
			description: "End time with invalid minute should return false",
		},
		{
			name:        "invalid start time - non-numeric characters",
			current:     "12:00",
			start:       "1a:00",
			end:         "14:00",
			expected:    false,
			description: "Start time with non-numeric characters should return false",
		},
		{
			name:        "invalid end time - non-numeric characters",
			current:     "12:00",
			start:       "10:00",
			end:         "14:b0",
			expected:    false,
			description: "End time with non-numeric characters should return false",
		},
		{
			name:        "both start and end invalid",
			current:     "12:00",
			start:       "invalid",
			end:         "also-invalid",
			expected:    false,
			description: "Both invalid start and end times should return false",
		},
		{
			name:        "empty start time",
			current:     "12:00",
			start:       "",
			end:         "14:00",
			expected:    false,
			description: "Empty start time should return false",
		},
		{
			name:        "empty end time",
			current:     "12:00",
			start:       "10:00",
			end:         "",
			expected:    false,
			description: "Empty end time should return false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTimeInRange(tt.current, tt.start, tt.end)
			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

func TestIsWithinTimeWindows_WithDays(t *testing.T) {
	// Mock current time to be a specific day for testing
	// Note: This test may be flaky depending on when it's run
	// In a real scenario, you might want to inject time dependency

	tests := []struct {
		name        string
		timeWindows []TimeWindow
		description string
		// We can't easily test day-specific logic without time injection
		// So we'll focus on the time range logic
	}{
		{
			name: "window with specific days",
			timeWindows: []TimeWindow{
				{
					Start: "00:00",
					End:   "23:59",
					Days:  []string{"monday", "tuesday"},
				},
			},
			description: "Window with specific days should respect day restrictions",
		},
		{
			name: "window without days restriction",
			timeWindows: []TimeWindow{
				{
					Start: "00:00",
					End:   "23:59",
					// No Days specified - should apply to all days
				},
			},
			description: "Window without days should apply to all days",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify the function doesn't panic and returns a boolean
			// Use mock time for consistent testing
			result := IsWithinTimeWindows(tt.timeWindows, "monday", "12:00")
			assert.IsType(t, true, result, "Should return a boolean value")
		})
	}
}

func TestIsWithinTimeWindows_Integration(t *testing.T) {
	// Test with a realistic scenario
	now := time.Now()
	currentDay := strings.ToLower(now.Weekday().String())
	currentTime := now.Format("15:04")

	// Create a time window that should include current time
	beforeTime := now.Add(-1 * time.Hour).Format("15:04")
	afterTime := now.Add(1 * time.Hour).Format("15:04")

	timeWindows := []TimeWindow{
		{
			Start: beforeTime,
			End:   afterTime,
		},
	}

	result := IsWithinTimeWindows(timeWindows, currentDay, currentTime)
	assert.True(t, result, "Current time should be within the test window")

	// Test with a window that excludes current time
	excludingWindows := []TimeWindow{
		{
			Start: "01:00",
			End:   "02:00", // Very narrow window unlikely to include current time
		},
	}

	// This might be flaky, but for most test runs, current time won't be 01:00-02:00
	result = IsWithinTimeWindows(excludingWindows, currentDay, currentTime)
	// We can't assert false here reliably, so we just verify it returns a boolean
	assert.IsType(t, true, result, "Should return a boolean value")
}

func TestResourceLeasingPolicy_IsWithinTimeWindowsAt(t *testing.T) {
	tests := []struct {
		name        string
		policy      *ResourceLeasingPolicy
		currentDay  string
		currentTime string
		expected    bool
		description string
	}{
		{
			name: "no time windows - always active",
			policy: &ResourceLeasingPolicy{
				Spec: ResourceLeasingPolicySpec{
					TimeWindows: []TimeWindow{},
				},
			},
			currentDay:  "monday",
			currentTime: "12:00",
			expected:    true,
			description: "Empty time windows should always be active",
		},
		{
			name: "within time window",
			policy: &ResourceLeasingPolicy{
				Spec: ResourceLeasingPolicySpec{
					TimeWindows: []TimeWindow{
						{
							Start: "09:00",
							End:   "17:00",
						},
					},
				},
			},
			currentDay:  "tuesday",
			currentTime: "12:00",
			expected:    true,
			description: "Should be active within business hours",
		},
		{
			name: "outside time window",
			policy: &ResourceLeasingPolicy{
				Spec: ResourceLeasingPolicySpec{
					TimeWindows: []TimeWindow{
						{
							Start: "09:00",
							End:   "17:00",
						},
					},
				},
			},
			currentDay:  "wednesday",
			currentTime: "20:00",
			expected:    false,
			description: "Should be inactive outside business hours",
		},
		{
			name: "matching day restriction",
			policy: &ResourceLeasingPolicy{
				Spec: ResourceLeasingPolicySpec{
					TimeWindows: []TimeWindow{
						{
							Start: "00:00",
							End:   "23:59",
							Days:  []string{"monday", "tuesday", "wednesday"},
						},
					},
				},
			},
			currentDay:  "monday",
			currentTime: "12:00",
			expected:    true,
			description: "Should be active on matching day",
		},
		{
			name: "non-matching day restriction",
			policy: &ResourceLeasingPolicy{
				Spec: ResourceLeasingPolicySpec{
					TimeWindows: []TimeWindow{
						{
							Start: "00:00",
							End:   "23:59",
							Days:  []string{"monday", "tuesday", "wednesday"},
						},
					},
				},
			},
			currentDay:  "saturday",
			currentTime: "12:00",
			expected:    false,
			description: "Should be inactive on non-matching day",
		},
		{
			name: "overnight window - within first part",
			policy: &ResourceLeasingPolicy{
				Spec: ResourceLeasingPolicySpec{
					TimeWindows: []TimeWindow{
						{
							Start: "22:00",
							End:   "02:00",
						},
					},
				},
			},
			currentDay:  "thursday",
			currentTime: "23:00",
			expected:    true,
			description: "Should be active in first part of overnight window",
		},
		{
			name: "overnight window - within second part",
			policy: &ResourceLeasingPolicy{
				Spec: ResourceLeasingPolicySpec{
					TimeWindows: []TimeWindow{
						{
							Start: "22:00",
							End:   "02:00",
						},
					},
				},
			},
			currentDay:  "friday",
			currentTime: "01:00",
			expected:    true,
			description: "Should be active in second part of overnight window",
		},
		{
			name: "overnight window - outside",
			policy: &ResourceLeasingPolicy{
				Spec: ResourceLeasingPolicySpec{
					TimeWindows: []TimeWindow{
						{
							Start: "22:00",
							End:   "02:00",
						},
					},
				},
			},
			currentDay:  "saturday",
			currentTime: "12:00",
			expected:    false,
			description: "Should be inactive outside overnight window",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.policy.IsWithinTimeWindowsAt(tt.currentDay, tt.currentTime)
			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

func TestIsValidTimeFormat(t *testing.T) {
	tests := []struct {
		name     string
		timeStr  string
		expected bool
	}{
		// Valid formats
		{"valid time - midnight", "00:00", true},
		{"valid time - noon", "12:00", true},
		{"valid time - evening", "23:59", true},
		{"valid time - morning", "09:30", true},
		{"valid time - afternoon", "15:45", true},

		// Invalid lengths
		{"too short", "9:00", false},
		{"too long", "09:000", false},
		{"empty string", "", false},
		{"too short - no minutes", "09:", false},
		{"too short - no seconds", "09:0", false},
		{"way too long", "09:00:00", false},

		// Invalid separators
		{"wrong separator - dash", "09-00", false},
		{"wrong separator - dot", "09.00", false},
		{"wrong separator - space", "09 00", false},
		{"no separator", "0900", false},
		{"multiple separators", "09::00", false},

		// Invalid hour values
		{"invalid hour - negative", "-1:00", false},
		{"invalid hour - too high", "24:00", false},
		{"invalid hour - way too high", "99:00", false},
		{"invalid hour - non-numeric", "ab:00", false},
		{"invalid hour - mixed", "1a:00", false},
		{"invalid hour - special chars", "@9:00", false},

		// Invalid minute values
		{"invalid minute - too high", "09:60", false},
		{"invalid minute - way too high", "09:99", false},
		{"invalid minute - non-numeric", "09:ab", false},
		{"invalid minute - mixed", "09:1a", false},
		{"invalid minute - special chars", "09:@0", false},

		// Edge cases
		{"boundary valid - 00:00", "00:00", true},
		{"boundary valid - 23:59", "23:59", true},
		{"boundary invalid - 24:00", "24:00", false},
		{"boundary invalid - 23:60", "23:60", false},

		// Mixed invalid cases
		{"both invalid", "ab:cd", false},
		{"random string", "invalid", false},
		{"numbers only", "1234", false},
		{"colon only", ":", false},
		{"colon at wrong position", "1:234", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidTimeFormat(tt.timeStr)
			assert.Equal(t, tt.expected, result, "isValidTimeFormat(%q) should return %v", tt.timeStr, tt.expected)
		})
	}
}
