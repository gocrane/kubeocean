package utils

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBoolPtr(t *testing.T) {
	tests := []struct {
		name  string
		input bool
		want  bool
	}{
		{
			name:  "true value",
			input: true,
			want:  true,
		},
		{
			name:  "false value",
			input: false,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BoolPtr(tt.input)
			assert.NotNil(t, result)
			assert.Equal(t, tt.want, *result)
		})
	}
}

func TestInt64Ptr(t *testing.T) {
	tests := []struct {
		name  string
		input int64
		want  int64
	}{
		{
			name:  "positive value",
			input: 42,
			want:  42,
		},
		{
			name:  "negative value",
			input: -10,
			want:  -10,
		},
		{
			name:  "zero value",
			input: 0,
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Int64Ptr(tt.input)
			assert.NotNil(t, result)
			assert.Equal(t, tt.want, *result)
		})
	}
}

func TestStringPtr(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "non-empty string",
			input: "hello",
			want:  "hello",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StringPtr(tt.input)
			assert.NotNil(t, result)
			assert.Equal(t, tt.want, *result)
		})
	}
}

func TestInt32Ptr(t *testing.T) {
	tests := []struct {
		name  string
		input int32
		want  int32
	}{
		{
			name:  "positive value",
			input: 100,
			want:  100,
		},
		{
			name:  "negative value",
			input: -50,
			want:  -50,
		},
		{
			name:  "zero value",
			input: 0,
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Int32Ptr(tt.input)
			assert.NotNil(t, result)
			assert.Equal(t, tt.want, *result)
		})
	}
}
func TestDurationPtr(t *testing.T) {
	tests := []struct {
		name  string
		input time.Duration
		want  time.Duration
	}{
		{
			name:  "positive duration",
			input: 5 * time.Minute,
			want:  5 * time.Minute,
		},
		{
			name:  "zero duration",
			input: 0,
			want:  0,
		},
		{
			name:  "negative duration",
			input: -1 * time.Second,
			want:  -1 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DurationPtr(tt.input)
			assert.NotNil(t, result)
			assert.Equal(t, tt.want, *result)
		})
	}
}
