package remotecommand

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	api "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	remotecommandconsts "k8s.io/apimachinery/pkg/util/remotecommand"
	"k8s.io/client-go/tools/remotecommand"
)

// mockStream implements httpstream.Stream for testing
type mockStream struct {
	headers    http.Header
	data       []byte
	writePos   int
	readPos    int
	closed     bool
	resetCalled bool
	identifier uint32
}

func newMockStream(streamType string) *mockStream {
	headers := http.Header{}
	headers.Set(api.StreamType, streamType)
	return &mockStream{
		headers: headers,
		data:    make([]byte, 0),
	}
}

func (m *mockStream) Read(p []byte) (n int, err error) {
	if m.readPos >= len(m.data) {
		return 0, io.EOF
	}
	n = copy(p, m.data[m.readPos:])
	m.readPos += n
	return n, nil
}

func (m *mockStream) Write(p []byte) (n int, err error) {
	if m.closed {
		return 0, errors.New("stream closed")
	}
	m.data = append(m.data, p...)
	m.writePos += len(p)
	return len(p), nil
}

func (m *mockStream) Close() error {
	m.closed = true
	return nil
}

func (m *mockStream) Reset() error {
	m.resetCalled = true
	return nil
}

func (m *mockStream) Headers() http.Header {
	return m.headers
}

func (m *mockStream) Identifier() uint32 {
	return m.identifier
}

// TestNewOptions_AllStreams tests creating Options with all streams enabled
func TestNewOptions_AllStreams(t *testing.T) {
	req := httptest.NewRequest("GET", "/exec?input=1&output=1&error=1&tty=0", nil)
	
	opts, err := NewOptions(req)
	
	require.NoError(t, err)
	assert.True(t, opts.Stdin)
	assert.True(t, opts.Stdout)
	assert.True(t, opts.Stderr)
	assert.False(t, opts.TTY)
}

// TestNewOptions_OnlyStdout tests creating Options with only stdout
func TestNewOptions_OnlyStdout(t *testing.T) {
	req := httptest.NewRequest("GET", "/exec?output=1", nil)
	
	opts, err := NewOptions(req)
	
	require.NoError(t, err)
	assert.False(t, opts.Stdin)
	assert.True(t, opts.Stdout)
	assert.False(t, opts.Stderr)
	assert.False(t, opts.TTY)
}

// TestNewOptions_WithTTY tests creating Options with TTY enabled
func TestNewOptions_WithTTY(t *testing.T) {
	req := httptest.NewRequest("GET", "/exec?input=1&output=1&tty=1", nil)
	
	opts, err := NewOptions(req)
	
	require.NoError(t, err)
	assert.True(t, opts.Stdin)
	assert.True(t, opts.Stdout)
	assert.False(t, opts.Stderr) // stderr should be false when TTY is enabled
	assert.True(t, opts.TTY)
}

// TestNewOptions_TTYWithStderr tests that stderr is disabled when TTY is enabled
func TestNewOptions_TTYWithStderr(t *testing.T) {
	req := httptest.NewRequest("GET", "/exec?input=1&output=1&error=1&tty=1", nil)
	
	opts, err := NewOptions(req)
	
	require.NoError(t, err)
	assert.True(t, opts.Stdin)
	assert.True(t, opts.Stdout)
	assert.False(t, opts.Stderr) // stderr should be bypassed when tty=1
	assert.True(t, opts.TTY)
}

// TestNewOptions_NoStreams tests that an error is returned when no streams are specified
func TestNewOptions_NoStreams(t *testing.T) {
	req := httptest.NewRequest("GET", "/exec", nil)
	
	opts, err := NewOptions(req)
	
	require.Error(t, err)
	assert.Nil(t, opts)
	assert.Contains(t, err.Error(), "you must specify at least 1 of stdin, stdout, stderr")
}

// TestNewOptions_StdinOnly tests creating Options with only stdin
func TestNewOptions_StdinOnly(t *testing.T) {
	req := httptest.NewRequest("GET", "/exec?input=1", nil)
	
	opts, err := NewOptions(req)
	
	require.NoError(t, err)
	assert.True(t, opts.Stdin)
	assert.False(t, opts.Stdout)
	assert.False(t, opts.Stderr)
	assert.False(t, opts.TTY)
}

// TestNewOptions_StderrOnly tests creating Options with only stderr
func TestNewOptions_StderrOnly(t *testing.T) {
	req := httptest.NewRequest("GET", "/exec?error=1", nil)
	
	opts, err := NewOptions(req)
	
	require.NoError(t, err)
	assert.False(t, opts.Stdin)
	assert.False(t, opts.Stdout)
	assert.True(t, opts.Stderr)
	assert.False(t, opts.TTY)
}

// TestNewOptions_InvalidValues tests creating Options with invalid parameter values
func TestNewOptions_InvalidValues(t *testing.T) {
	testCases := []struct {
		name        string
		queryString string
		expectError bool
	}{
		{"ZeroValues", "input=0&output=0&error=0", true},
		{"EmptyValues", "input=&output=&error=", true},
		{"NonOneValues", "input=2&output=2", true}, // Should be treated as false, thus no valid streams
		{"MixedValid", "input=1&output=0&error=1", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/exec?"+tc.queryString, nil)
			opts, err := NewOptions(req)
			
			if tc.expectError {
				assert.Error(t, err)
				assert.Nil(t, opts)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, opts)
			}
		})
	}
}

// TestWaitStreamReply_ReplySent tests waitStreamReply when replySent is closed
func TestWaitStreamReply_ReplySent(t *testing.T) {
	replySent := make(chan struct{})
	notify := make(chan struct{}, 1)
	stop := make(chan struct{})

	close(replySent)
	
	go waitStreamReply(replySent, notify, stop)
	
	select {
	case <-notify:
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Fatal("waitStreamReply did not send to notify channel")
	}
}

// TestWaitStreamReply_Stopped tests waitStreamReply when stop is closed
func TestWaitStreamReply_Stopped(t *testing.T) {
	replySent := make(chan struct{})
	notify := make(chan struct{}, 1)
	stop := make(chan struct{})

	close(stop)
	
	go waitStreamReply(replySent, notify, stop)
	
	select {
	case <-notify:
		t.Fatal("waitStreamReply should not send to notify when stopped")
	case <-time.After(100 * time.Millisecond):
		// Success - should not send notification
	}
}

// TestHandleResizeEvents_ValidJSON tests handleResizeEvents with valid JSON
func TestHandleResizeEvents_ValidJSON(t *testing.T) {
	size1 := remotecommand.TerminalSize{Width: 80, Height: 24}
	size2 := remotecommand.TerminalSize{Width: 120, Height: 30}
	
	data1, _ := json.Marshal(size1)
	data2, _ := json.Marshal(size2)
	
	reader := strings.NewReader(string(data1) + "\n" + string(data2) + "\n")
	channel := make(chan remotecommand.TerminalSize, 2)
	
	go handleResizeEvents(reader, channel)
	
	// Read first size
	select {
	case s := <-channel:
		assert.Equal(t, uint16(80), s.Width)
		assert.Equal(t, uint16(24), s.Height)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for first resize event")
	}
	
	// Read second size
	select {
	case s := <-channel:
		assert.Equal(t, uint16(120), s.Width)
		assert.Equal(t, uint16(30), s.Height)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for second resize event")
	}
	
	// Channel should be closed after EOF
	select {
	case _, ok := <-channel:
		assert.False(t, ok, "channel should be closed")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel was not closed")
	}
}

// TestHandleResizeEvents_InvalidJSON tests handleResizeEvents with invalid JSON
func TestHandleResizeEvents_InvalidJSON(t *testing.T) {
	reader := strings.NewReader("invalid json data\n")
	channel := make(chan remotecommand.TerminalSize, 1)
	
	go handleResizeEvents(reader, channel)
	
	// Channel should be closed after error
	select {
	case _, ok := <-channel:
		assert.False(t, ok, "channel should be closed on error")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel was not closed after error")
	}
}

// TestHandleResizeEvents_EmptyStream tests handleResizeEvents with empty stream
func TestHandleResizeEvents_EmptyStream(t *testing.T) {
	reader := strings.NewReader("")
	channel := make(chan remotecommand.TerminalSize, 1)
	
	go handleResizeEvents(reader, channel)
	
	// Channel should be closed immediately
	select {
	case _, ok := <-channel:
		assert.False(t, ok, "channel should be closed")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel was not closed")
	}
}

// TestV1WriteStatusFunc_Success tests v1WriteStatusFunc with success status
func TestV1WriteStatusFunc_Success(t *testing.T) {
	stream := newMockStream(api.StreamTypeError)
	writeStatus := v1WriteStatusFunc(stream)
	
	statusErr := &apierrors.StatusError{
		ErrStatus: metav1.Status{
			Status: metav1.StatusSuccess,
		},
	}
	
	err := writeStatus(statusErr)
	
	assert.NoError(t, err)
	assert.Equal(t, 0, len(stream.data)) // Should not write for success
}

// TestV1WriteStatusFunc_Failure tests v1WriteStatusFunc with failure status
func TestV1WriteStatusFunc_Failure(t *testing.T) {
	stream := newMockStream(api.StreamTypeError)
	writeStatus := v1WriteStatusFunc(stream)
	
	statusErr := &apierrors.StatusError{
		ErrStatus: metav1.Status{
			Status:  metav1.StatusFailure,
			Message: "command failed",
		},
	}
	
	err := writeStatus(statusErr)
	
	assert.NoError(t, err)
	assert.Contains(t, string(stream.data), "command failed")
}

// TestV4WriteStatusFunc_Success tests v4WriteStatusFunc with success status
func TestV4WriteStatusFunc_Success(t *testing.T) {
	stream := newMockStream(api.StreamTypeError)
	writeStatus := v4WriteStatusFunc(stream)
	
	statusErr := &apierrors.StatusError{
		ErrStatus: metav1.Status{
			Status: metav1.StatusSuccess,
		},
	}
	
	err := writeStatus(statusErr)
	
	assert.NoError(t, err)
	
	// Verify JSON marshaling
	var status metav1.Status
	err = json.Unmarshal(stream.data, &status)
	assert.NoError(t, err)
	assert.Equal(t, metav1.StatusSuccess, status.Status)
}

// TestV4WriteStatusFunc_FailureWithExitCode tests v4WriteStatusFunc with exit code
func TestV4WriteStatusFunc_FailureWithExitCode(t *testing.T) {
	stream := newMockStream(api.StreamTypeError)
	writeStatus := v4WriteStatusFunc(stream)
	
	statusErr := &apierrors.StatusError{
		ErrStatus: metav1.Status{
			Status: metav1.StatusFailure,
			Reason: remotecommandconsts.NonZeroExitCodeReason,
			Details: &metav1.StatusDetails{
				Causes: []metav1.StatusCause{
					{
						Type:    remotecommandconsts.ExitCodeCauseType,
						Message: "127",
					},
				},
			},
			Message: "command terminated with non-zero exit code",
		},
	}
	
	err := writeStatus(statusErr)
	
	assert.NoError(t, err)
	
	// Verify JSON marshaling
	var status metav1.Status
	err = json.Unmarshal(stream.data, &status)
	assert.NoError(t, err)
	assert.Equal(t, metav1.StatusFailure, status.Status)
	assert.Equal(t, remotecommandconsts.NonZeroExitCodeReason, status.Reason)
	assert.Equal(t, 1, len(status.Details.Causes))
	assert.Equal(t, "127", status.Details.Causes[0].Message)
}

// TestV4ProtocolHandler_SupportsTerminalResizing tests v4 protocol resize support
func TestV4ProtocolHandler_SupportsTerminalResizing(t *testing.T) {
	handler := &v4ProtocolHandler{}
	assert.True(t, handler.supportsTerminalResizing())
}

// TestV3ProtocolHandler_SupportsTerminalResizing tests v3 protocol resize support
func TestV3ProtocolHandler_SupportsTerminalResizing(t *testing.T) {
	handler := &v3ProtocolHandler{}
	assert.True(t, handler.supportsTerminalResizing())
}

// TestV2ProtocolHandler_NoResizeSupport tests v2 protocol does not support resize
func TestV2ProtocolHandler_NoResizeSupport(t *testing.T) {
	handler := &v2ProtocolHandler{}
	assert.False(t, handler.supportsTerminalResizing())
}

// TestV1ProtocolHandler_NoResizeSupport tests v1 protocol does not support resize
func TestV1ProtocolHandler_NoResizeSupport(t *testing.T) {
	handler := &v1ProtocolHandler{}
	assert.False(t, handler.supportsTerminalResizing())
}

// TestWaitForStreamsWithWriteStatusFunc_AllStreams tests waiting for all expected streams
func TestWaitForStreamsWithWriteStatusFunc_AllStreams(t *testing.T) {
	streamCh := make(chan streamAndReply, 5)
	replySent := make(chan struct{})
	close(replySent)
	
	// Send all expected streams
	streamCh <- streamAndReply{Stream: newMockStream(api.StreamTypeError), replySent: replySent}
	streamCh <- streamAndReply{Stream: newMockStream(api.StreamTypeStdin), replySent: replySent}
	streamCh <- streamAndReply{Stream: newMockStream(api.StreamTypeStdout), replySent: replySent}
	streamCh <- streamAndReply{Stream: newMockStream(api.StreamTypeStderr), replySent: replySent}
	streamCh <- streamAndReply{Stream: newMockStream(api.StreamTypeResize), replySent: replySent}
	
	expired := make(chan time.Time)
	
	ctx, err := waitForStreamsWithWriteStatusFunc(streamCh, 5, expired, v4WriteStatusFunc)
	
	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.NotNil(t, ctx.writeStatus)
	assert.NotNil(t, ctx.stdinStream)
	assert.NotNil(t, ctx.stdoutStream)
	assert.NotNil(t, ctx.stderrStream)
	assert.NotNil(t, ctx.resizeStream)
}

// TestWaitForStreamsWithWriteStatusFunc_Timeout tests timeout waiting for streams
func TestWaitForStreamsWithWriteStatusFunc_Timeout(t *testing.T) {
	streamCh := make(chan streamAndReply, 5)
	replySent := make(chan struct{})
	
	// Send only one stream
	streamCh <- streamAndReply{Stream: newMockStream(api.StreamTypeError), replySent: replySent}
	close(replySent)
	
	expired := make(chan time.Time)
	close(expired) // Trigger timeout immediately
	
	ctx, err := waitForStreamsWithWriteStatusFunc(streamCh, 5, expired, v4WriteStatusFunc)
	
	require.Error(t, err)
	assert.Nil(t, ctx)
	assert.Contains(t, err.Error(), "timed out waiting for client to create streams")
}

// TestWaitForStreamsWithWriteStatusFunc_PartialStreams tests receiving only some streams
func TestWaitForStreamsWithWriteStatusFunc_PartialStreams(t *testing.T) {
	streamCh := make(chan streamAndReply, 3)
	replySent := make(chan struct{})
	close(replySent)
	
	// Send only stdin and stdout
	streamCh <- streamAndReply{Stream: newMockStream(api.StreamTypeError), replySent: replySent}
	streamCh <- streamAndReply{Stream: newMockStream(api.StreamTypeStdin), replySent: replySent}
	streamCh <- streamAndReply{Stream: newMockStream(api.StreamTypeStdout), replySent: replySent}
	
	expired := make(chan time.Time)
	
	ctx, err := waitForStreamsWithWriteStatusFunc(streamCh, 3, expired, v4WriteStatusFunc)
	
	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.NotNil(t, ctx.stdinStream)
	assert.NotNil(t, ctx.stdoutStream)
	assert.Nil(t, ctx.stderrStream)
	assert.Nil(t, ctx.resizeStream)
}

// TestMockStream_ReadWrite tests the mockStream read/write functionality
func TestMockStream_ReadWrite(t *testing.T) {
	stream := newMockStream(api.StreamTypeStdout)
	
	// Test write
	n, err := stream.Write([]byte("hello world"))
	assert.NoError(t, err)
	assert.Equal(t, 11, n)
	
	// Test read
	buf := make([]byte, 5)
	n, err = stream.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, []byte("hello"), buf)
	
	// Read remaining
	buf = make([]byte, 10)
	n, err = stream.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, 6, n)
	assert.Equal(t, []byte(" world"), buf[:n])
	
	// Read at EOF
	n, err = stream.Read(buf)
	assert.Equal(t, 0, n)
	assert.Equal(t, io.EOF, err)
}

// TestMockStream_Close tests stream closing
func TestMockStream_Close(t *testing.T) {
	stream := newMockStream(api.StreamTypeStdout)
	
	err := stream.Close()
	assert.NoError(t, err)
	assert.True(t, stream.closed)
	
	// Write after close should fail
	_, err = stream.Write([]byte("test"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stream closed")
}

// TestMockStream_Reset tests stream reset
func TestMockStream_Reset(t *testing.T) {
	stream := newMockStream(api.StreamTypeStdout)
	
	err := stream.Reset()
	assert.NoError(t, err)
	assert.True(t, stream.resetCalled)
}

// TestMockStream_Headers tests stream headers
func TestMockStream_Headers(t *testing.T) {
	stream := newMockStream(api.StreamTypeStdout)
	
	headers := stream.Headers()
	assert.NotNil(t, headers)
	assert.Equal(t, api.StreamTypeStdout, headers.Get(api.StreamType))
}

// TestMockStream_Identifier tests stream identifier
func TestMockStream_Identifier(t *testing.T) {
	stream := newMockStream(api.StreamTypeStdout)
	stream.identifier = 42
	
	assert.Equal(t, uint32(42), stream.Identifier())
}

// TestOptions_AllCombinations tests all valid combinations of Options
func TestOptions_AllCombinations(t *testing.T) {
	testCases := []struct {
		name   string
		params url.Values
		stdin  bool
		stdout bool
		stderr bool
		tty    bool
		valid  bool
	}{
		{"All", url.Values{"input": {"1"}, "output": {"1"}, "error": {"1"}}, true, true, true, false, true},
		{"StdinStdout", url.Values{"input": {"1"}, "output": {"1"}}, true, true, false, false, true},
		{"StdinStderr", url.Values{"input": {"1"}, "error": {"1"}}, true, false, true, false, true},
		{"StdoutStderr", url.Values{"output": {"1"}, "error": {"1"}}, false, true, true, false, true},
		{"StdinOnly", url.Values{"input": {"1"}}, true, false, false, false, true},
		{"StdoutOnly", url.Values{"output": {"1"}}, false, true, false, false, true},
		{"StderrOnly", url.Values{"error": {"1"}}, false, false, true, false, true},
		{"TTYWithAll", url.Values{"input": {"1"}, "output": {"1"}, "error": {"1"}, "tty": {"1"}}, true, true, false, true, true},
		{"TTYNoStderr", url.Values{"input": {"1"}, "output": {"1"}, "tty": {"1"}}, true, true, false, true, true},
		{"None", url.Values{}, false, false, false, false, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := &http.Request{Form: tc.params}
			opts, err := NewOptions(req)
			
			if tc.valid {
				require.NoError(t, err)
				assert.Equal(t, tc.stdin, opts.Stdin)
				assert.Equal(t, tc.stdout, opts.Stdout)
				assert.Equal(t, tc.stderr, opts.Stderr)
				assert.Equal(t, tc.tty, opts.TTY)
			} else {
				require.Error(t, err)
			}
		})
	}
}
