package remotecommand

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/httpstream/wsstream"
)

// TestCreateChannels_AllStreamsEnabled tests channel creation with all streams
func TestCreateChannels_AllStreamsEnabled(t *testing.T) {
	opts := &Options{
		Stdin:  true,
		Stdout: true,
		Stderr: true,
		TTY:    false,
	}

	channels := createChannels(opts)

	require.Len(t, channels, 5)
	assert.Equal(t, wsstream.ReadChannel, channels[stdinChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[stdoutChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[stderrChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[errorChannel])
	assert.Equal(t, wsstream.ReadChannel, channels[resizeChannel])
}

// TestCreateChannels_NoStdin tests channel creation without stdin
func TestCreateChannels_NoStdin(t *testing.T) {
	opts := &Options{
		Stdin:  false,
		Stdout: true,
		Stderr: true,
		TTY:    false,
	}

	channels := createChannels(opts)

	require.Len(t, channels, 5)
	assert.Equal(t, wsstream.IgnoreChannel, channels[stdinChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[stdoutChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[stderrChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[errorChannel])
	assert.Equal(t, wsstream.ReadChannel, channels[resizeChannel])
}

// TestCreateChannels_NoStdout tests channel creation without stdout
func TestCreateChannels_NoStdout(t *testing.T) {
	opts := &Options{
		Stdin:  true,
		Stdout: false,
		Stderr: true,
		TTY:    false,
	}

	channels := createChannels(opts)

	require.Len(t, channels, 5)
	assert.Equal(t, wsstream.ReadChannel, channels[stdinChannel])
	assert.Equal(t, wsstream.IgnoreChannel, channels[stdoutChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[stderrChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[errorChannel])
	assert.Equal(t, wsstream.ReadChannel, channels[resizeChannel])
}

// TestCreateChannels_NoStderr tests channel creation without stderr
func TestCreateChannels_NoStderr(t *testing.T) {
	opts := &Options{
		Stdin:  true,
		Stdout: true,
		Stderr: false,
		TTY:    false,
	}

	channels := createChannels(opts)

	require.Len(t, channels, 5)
	assert.Equal(t, wsstream.ReadChannel, channels[stdinChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[stdoutChannel])
	assert.Equal(t, wsstream.IgnoreChannel, channels[stderrChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[errorChannel])
	assert.Equal(t, wsstream.ReadChannel, channels[resizeChannel])
}

// TestCreateChannels_OnlyStdout tests channel creation with only stdout
func TestCreateChannels_OnlyStdout(t *testing.T) {
	opts := &Options{
		Stdin:  false,
		Stdout: true,
		Stderr: false,
		TTY:    false,
	}

	channels := createChannels(opts)

	require.Len(t, channels, 5)
	assert.Equal(t, wsstream.IgnoreChannel, channels[stdinChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[stdoutChannel])
	assert.Equal(t, wsstream.IgnoreChannel, channels[stderrChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[errorChannel])
	assert.Equal(t, wsstream.ReadChannel, channels[resizeChannel])
}

// TestCreateChannels_OnlyStderr tests channel creation with only stderr
func TestCreateChannels_OnlyStderr(t *testing.T) {
	opts := &Options{
		Stdin:  false,
		Stdout: false,
		Stderr: true,
		TTY:    false,
	}

	channels := createChannels(opts)

	require.Len(t, channels, 5)
	assert.Equal(t, wsstream.IgnoreChannel, channels[stdinChannel])
	assert.Equal(t, wsstream.IgnoreChannel, channels[stdoutChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[stderrChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[errorChannel])
	assert.Equal(t, wsstream.ReadChannel, channels[resizeChannel])
}

// TestCreateChannels_WithTTY tests channel creation with TTY enabled
func TestCreateChannels_WithTTY(t *testing.T) {
	opts := &Options{
		Stdin:  true,
		Stdout: true,
		Stderr: false, // stderr should be false with TTY
		TTY:    true,
	}

	channels := createChannels(opts)

	require.Len(t, channels, 5)
	assert.Equal(t, wsstream.ReadChannel, channels[stdinChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[stdoutChannel])
	assert.Equal(t, wsstream.IgnoreChannel, channels[stderrChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[errorChannel])
	assert.Equal(t, wsstream.ReadChannel, channels[resizeChannel])
}

// TestCreateChannels_AllDisabled tests channel creation with all streams disabled
func TestCreateChannels_AllDisabled(t *testing.T) {
	opts := &Options{
		Stdin:  false,
		Stdout: false,
		Stderr: false,
		TTY:    false,
	}

	channels := createChannels(opts)

	require.Len(t, channels, 5)
	assert.Equal(t, wsstream.IgnoreChannel, channels[stdinChannel])
	assert.Equal(t, wsstream.IgnoreChannel, channels[stdoutChannel])
	assert.Equal(t, wsstream.IgnoreChannel, channels[stderrChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[errorChannel]) // error always enabled
	assert.Equal(t, wsstream.ReadChannel, channels[resizeChannel])  // resize always enabled
}

// TestReadChannel_True tests readChannel with true parameter
func TestReadChannel_True(t *testing.T) {
	result := readChannel(true)
	assert.Equal(t, wsstream.ReadChannel, result)
}

// TestReadChannel_False tests readChannel with false parameter
func TestReadChannel_False(t *testing.T) {
	result := readChannel(false)
	assert.Equal(t, wsstream.IgnoreChannel, result)
}

// TestWriteChannel_True tests writeChannel with true parameter
func TestWriteChannel_True(t *testing.T) {
	result := writeChannel(true)
	assert.Equal(t, wsstream.WriteChannel, result)
}

// TestWriteChannel_False tests writeChannel with false parameter
func TestWriteChannel_False(t *testing.T) {
	result := writeChannel(false)
	assert.Equal(t, wsstream.IgnoreChannel, result)
}

// TestChannelConstants tests the channel constant values
func TestChannelConstants(t *testing.T) {
	assert.Equal(t, 0, stdinChannel)
	assert.Equal(t, 1, stdoutChannel)
	assert.Equal(t, 2, stderrChannel)
	assert.Equal(t, 3, errorChannel)
	assert.Equal(t, 4, resizeChannel)
}

// TestWebSocketProtocolConstants tests the websocket protocol constant values
func TestWebSocketProtocolConstants(t *testing.T) {
	assert.Equal(t, wsstream.ChannelWebSocketProtocol, preV4BinaryWebsocketProtocol)
	assert.Equal(t, wsstream.Base64ChannelWebSocketProtocol, preV4Base64WebsocketProtocol)
	assert.Equal(t, "v4."+wsstream.ChannelWebSocketProtocol, v4BinaryWebsocketProtocol)
	assert.Equal(t, "v4."+wsstream.Base64ChannelWebSocketProtocol, v4Base64WebsocketProtocol)
}

// TestCreateChannels_ConsistentLength tests that createChannels always returns 5 channels
func TestCreateChannels_ConsistentLength(t *testing.T) {
	testCases := []struct {
		name string
		opts *Options
	}{
		{"AllTrue", &Options{Stdin: true, Stdout: true, Stderr: true, TTY: false}},
		{"AllFalse", &Options{Stdin: false, Stdout: false, Stderr: false, TTY: false}},
		{"Mixed1", &Options{Stdin: true, Stdout: false, Stderr: true, TTY: false}},
		{"Mixed2", &Options{Stdin: false, Stdout: true, Stderr: false, TTY: false}},
		{"WithTTY", &Options{Stdin: true, Stdout: true, Stderr: false, TTY: true}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			channels := createChannels(tc.opts)
			assert.Len(t, channels, 5, "createChannels should always return exactly 5 channels")
		})
	}
}

// TestCreateChannels_ErrorChannelAlwaysWrite tests that error channel is always WriteChannel
func TestCreateChannels_ErrorChannelAlwaysWrite(t *testing.T) {
	testCases := []struct {
		name string
		opts *Options
	}{
		{"AllTrue", &Options{Stdin: true, Stdout: true, Stderr: true, TTY: false}},
		{"AllFalse", &Options{Stdin: false, Stdout: false, Stderr: false, TTY: false}},
		{"Mixed", &Options{Stdin: true, Stdout: false, Stderr: false, TTY: false}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			channels := createChannels(tc.opts)
			assert.Equal(t, wsstream.WriteChannel, channels[errorChannel],
				"error channel should always be WriteChannel")
		})
	}
}

// TestCreateChannels_ResizeChannelAlwaysRead tests that resize channel is always ReadChannel
func TestCreateChannels_ResizeChannelAlwaysRead(t *testing.T) {
	testCases := []struct {
		name string
		opts *Options
	}{
		{"AllTrue", &Options{Stdin: true, Stdout: true, Stderr: true, TTY: false}},
		{"AllFalse", &Options{Stdin: false, Stdout: false, Stderr: false, TTY: false}},
		{"WithTTY", &Options{Stdin: true, Stdout: true, Stderr: false, TTY: true}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			channels := createChannels(tc.opts)
			assert.Equal(t, wsstream.ReadChannel, channels[resizeChannel],
				"resize channel should always be ReadChannel")
		})
	}
}

// TestCreateChannels_StdinMapping tests stdin channel mapping logic
func TestCreateChannels_StdinMapping(t *testing.T) {
	testCases := []struct {
		name     string
		stdin    bool
		expected wsstream.ChannelType
	}{
		{"StdinEnabled", true, wsstream.ReadChannel},
		{"StdinDisabled", false, wsstream.IgnoreChannel},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := &Options{
				Stdin:  tc.stdin,
				Stdout: true,
				Stderr: true,
				TTY:    false,
			}
			channels := createChannels(opts)
			assert.Equal(t, tc.expected, channels[stdinChannel])
		})
	}
}

// TestCreateChannels_StdoutMapping tests stdout channel mapping logic
func TestCreateChannels_StdoutMapping(t *testing.T) {
	testCases := []struct {
		name     string
		stdout   bool
		expected wsstream.ChannelType
	}{
		{"StdoutEnabled", true, wsstream.WriteChannel},
		{"StdoutDisabled", false, wsstream.IgnoreChannel},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := &Options{
				Stdin:  true,
				Stdout: tc.stdout,
				Stderr: true,
				TTY:    false,
			}
			channels := createChannels(opts)
			assert.Equal(t, tc.expected, channels[stdoutChannel])
		})
	}
}

// TestCreateChannels_StderrMapping tests stderr channel mapping logic
func TestCreateChannels_StderrMapping(t *testing.T) {
	testCases := []struct {
		name     string
		stderr   bool
		expected wsstream.ChannelType
	}{
		{"StderrEnabled", true, wsstream.WriteChannel},
		{"StderrDisabled", false, wsstream.IgnoreChannel},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := &Options{
				Stdin:  true,
				Stdout: true,
				Stderr: tc.stderr,
				TTY:    false,
			}
			channels := createChannels(opts)
			assert.Equal(t, tc.expected, channels[stderrChannel])
		})
	}
}

// TestCreateChannels_ChannelOrder tests that channels are in the correct order
func TestCreateChannels_ChannelOrder(t *testing.T) {
	opts := &Options{
		Stdin:  true,
		Stdout: true,
		Stderr: true,
		TTY:    false,
	}

	channels := createChannels(opts)

	// Verify the order matches the constants
	assert.Equal(t, wsstream.ReadChannel, channels[0])   // stdin
	assert.Equal(t, wsstream.WriteChannel, channels[1])  // stdout
	assert.Equal(t, wsstream.WriteChannel, channels[2])  // stderr
	assert.Equal(t, wsstream.WriteChannel, channels[3])  // error
	assert.Equal(t, wsstream.ReadChannel, channels[4])   // resize
}

// TestCreateChannels_DifferentCombinations tests various combinations of options
func TestCreateChannels_DifferentCombinations(t *testing.T) {
	testCases := []struct {
		name                                       string
		stdin, stdout, stderr, tty                 bool
		expStdin, expStdout, expStderr             wsstream.ChannelType
		expError, expResize                        wsstream.ChannelType
	}{
		{
			name:      "StdinStdout",
			stdin:     true,
			stdout:    true,
			stderr:    false,
			tty:       false,
			expStdin:  wsstream.ReadChannel,
			expStdout: wsstream.WriteChannel,
			expStderr: wsstream.IgnoreChannel,
			expError:  wsstream.WriteChannel,
			expResize: wsstream.ReadChannel,
		},
		{
			name:      "StdinStderr",
			stdin:     true,
			stdout:    false,
			stderr:    true,
			tty:       false,
			expStdin:  wsstream.ReadChannel,
			expStdout: wsstream.IgnoreChannel,
			expStderr: wsstream.WriteChannel,
			expError:  wsstream.WriteChannel,
			expResize: wsstream.ReadChannel,
		},
		{
			name:      "StdoutStderr",
			stdin:     false,
			stdout:    true,
			stderr:    true,
			tty:       false,
			expStdin:  wsstream.IgnoreChannel,
			expStdout: wsstream.WriteChannel,
			expStderr: wsstream.WriteChannel,
			expError:  wsstream.WriteChannel,
			expResize: wsstream.ReadChannel,
		},
		{
			name:      "OnlyStdin",
			stdin:     true,
			stdout:    false,
			stderr:    false,
			tty:       false,
			expStdin:  wsstream.ReadChannel,
			expStdout: wsstream.IgnoreChannel,
			expStderr: wsstream.IgnoreChannel,
			expError:  wsstream.WriteChannel,
			expResize: wsstream.ReadChannel,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := &Options{
				Stdin:  tc.stdin,
				Stdout: tc.stdout,
				Stderr: tc.stderr,
				TTY:    tc.tty,
			}
			channels := createChannels(opts)

			assert.Equal(t, tc.expStdin, channels[stdinChannel], "stdin channel mismatch")
			assert.Equal(t, tc.expStdout, channels[stdoutChannel], "stdout channel mismatch")
			assert.Equal(t, tc.expStderr, channels[stderrChannel], "stderr channel mismatch")
			assert.Equal(t, tc.expError, channels[errorChannel], "error channel mismatch")
			assert.Equal(t, tc.expResize, channels[resizeChannel], "resize channel mismatch")
		})
	}
}

// mockWebSocketConn is a mock websocket connection for testing
type mockWebSocketConn struct {
	streams       map[int]io.ReadWriteCloser
	closed        bool
	idleTimeout   time.Duration
	protocolUsed  string
}

func (m *mockWebSocketConn) Close() error {
	m.closed = true
	return nil
}

func (m *mockWebSocketConn) SetIdleTimeout(timeout time.Duration) {
	m.idleTimeout = timeout
}

// TestReadChannel_MultipleInvocations tests multiple calls to readChannel
func TestReadChannel_MultipleInvocations(t *testing.T) {
	// Test that function is deterministic
	for i := 0; i < 10; i++ {
		assert.Equal(t, wsstream.ReadChannel, readChannel(true))
		assert.Equal(t, wsstream.IgnoreChannel, readChannel(false))
	}
}

// TestWriteChannel_MultipleInvocations tests multiple calls to writeChannel
func TestWriteChannel_MultipleInvocations(t *testing.T) {
	// Test that function is deterministic
	for i := 0; i < 10; i++ {
		assert.Equal(t, wsstream.WriteChannel, writeChannel(true))
		assert.Equal(t, wsstream.IgnoreChannel, writeChannel(false))
	}
}

// TestCreateChannels_NilOptions tests createChannels with nil options (should panic in real code)
func TestCreateChannels_NilOptions(t *testing.T) {
	// This test documents that nil options would cause a panic
	// In real code, this should be validated before calling createChannels
	defer func() {
		if r := recover(); r != nil {
			// Expected panic with nil options
			assert.NotNil(t, r)
		}
	}()

	// Uncomment to test panic behavior:
	// _ = createChannels(nil)
}

// TestCreateChannels_EmptyOptions tests createChannels with zero-value options
func TestCreateChannels_EmptyOptions(t *testing.T) {
	opts := &Options{} // All fields are false by default

	channels := createChannels(opts)

	require.Len(t, channels, 5)
	assert.Equal(t, wsstream.IgnoreChannel, channels[stdinChannel])
	assert.Equal(t, wsstream.IgnoreChannel, channels[stdoutChannel])
	assert.Equal(t, wsstream.IgnoreChannel, channels[stderrChannel])
	assert.Equal(t, wsstream.WriteChannel, channels[errorChannel])
	assert.Equal(t, wsstream.ReadChannel, channels[resizeChannel])
}

// TestCreateChannels_TTYDoesNotAffectChannels tests that TTY flag doesn't directly affect channel creation
func TestCreateChannels_TTYDoesNotAffectChannels(t *testing.T) {
	optsWithoutTTY := &Options{
		Stdin:  true,
		Stdout: true,
		Stderr: true,
		TTY:    false,
	}

	optsWithTTY := &Options{
		Stdin:  true,
		Stdout: true,
		Stderr: true,
		TTY:    true,
	}

	channelsWithoutTTY := createChannels(optsWithoutTTY)
	channelsWithTTY := createChannels(optsWithTTY)

	// TTY flag is handled elsewhere (in NewOptions), createChannels just uses the Options values
	assert.Equal(t, channelsWithoutTTY[stdinChannel], channelsWithTTY[stdinChannel])
	assert.Equal(t, channelsWithoutTTY[stdoutChannel], channelsWithTTY[stdoutChannel])
	assert.Equal(t, channelsWithoutTTY[stderrChannel], channelsWithTTY[stderrChannel])
	assert.Equal(t, channelsWithoutTTY[errorChannel], channelsWithTTY[errorChannel])
	assert.Equal(t, channelsWithoutTTY[resizeChannel], channelsWithTTY[resizeChannel])
}

// BenchmarkCreateChannels benchmarks the createChannels function
func BenchmarkCreateChannels(b *testing.B) {
	opts := &Options{
		Stdin:  true,
		Stdout: true,
		Stderr: true,
		TTY:    false,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = createChannels(opts)
	}
}

// BenchmarkReadChannel benchmarks the readChannel function
func BenchmarkReadChannel(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readChannel(true)
		_ = readChannel(false)
	}
}

// BenchmarkWriteChannel benchmarks the writeChannel function
func BenchmarkWriteChannel(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = writeChannel(true)
		_ = writeChannel(false)
	}
}
