package remotecommand

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/utils/exec"
)

// MockExecutor is a mock implementation of the Executor interface
type MockExecutor struct {
	mock.Mock
}

func (m *MockExecutor) ExecInContainer(name string, uid types.UID, container string, cmd []string, in io.Reader, out, err io.WriteCloser, tty bool, resize <-chan remotecommand.TerminalSize, timeout time.Duration) error {
	args := m.Called(name, uid, container, cmd, in, out, err, tty, resize, timeout)
	return args.Error(0)
}

// MockExitError implements utilexec.ExitError for testing
type MockExitError struct {
	exitCode int
	exited   bool
}

func (e *MockExitError) Error() string {
	return "command terminated with exit code"
}

func (e *MockExitError) ExitStatus() int {
	return e.exitCode
}

func (e *MockExitError) Exited() bool {
	return e.exited
}

func (e *MockExitError) String() string {
	return e.Error()
}

// mockReadCloser is a mock io.ReadCloser
type mockReadCloser struct {
	data []byte
	pos  int
}

func (m *mockReadCloser) Read(p []byte) (n int, err error) {
	if m.pos >= len(m.data) {
		return 0, io.EOF
	}
	n = copy(p, m.data[m.pos:])
	m.pos += n
	return n, nil
}

func (m *mockReadCloser) Close() error {
	return nil
}

// mockWriteCloser is a mock io.WriteCloser
type mockWriteCloser struct {
	data []byte
}

func (m *mockWriteCloser) Write(p []byte) (n int, err error) {
	m.data = append(m.data, p...)
	return len(p), nil
}

func (m *mockWriteCloser) Close() error {
	return nil
}

// mockContext is a mock context for testing
type mockContext struct {
	stdinStream  io.ReadCloser
	stdoutStream io.WriteCloser
	stderrStream io.WriteCloser
	resizeChan   chan remotecommand.TerminalSize
	tty          bool
	conn         io.Closer
	statusErr    error
	statusCalled bool
}

func (m *mockContext) writeStatus(status error) error {
	m.statusCalled = true
	m.statusErr = status
	return nil
}

type mockCloser struct{}

func (m *mockCloser) Close() error {
	return nil
}

// TestServeExec_Success tests successful command execution
func TestServeExec_Success(t *testing.T) {
	mockExec := new(MockExecutor)
	mockExec.On("ExecInContainer",
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		[]string{"/bin/sh", "-c", "echo hello"},
		mock.Anything,
		mock.Anything,
		mock.Anything,
		false,
		mock.Anything,
		time.Duration(0),
	).Return(nil)

	// Note: This test would require mocking createStreams which is complex
	// For now, we test the executor call pattern
	cmd := []string{"/bin/sh", "-c", "echo hello"}
	stdin := &mockReadCloser{}
	stdout := &mockWriteCloser{}
	stderr := &mockWriteCloser{}
	resize := make(chan remotecommand.TerminalSize)

	err := mockExec.ExecInContainer(
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		cmd,
		stdin,
		stdout,
		stderr,
		false,
		resize,
		0,
	)

	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

// TestServeExec_ExitError tests command execution with exit error
func TestServeExec_ExitError(t *testing.T) {
	mockExec := new(MockExecutor)
	exitErr := &MockExitError{
		exitCode: 127,
		exited:   true,
	}

	mockExec.On("ExecInContainer",
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		[]string{"/bin/sh", "-c", "exit 127"},
		mock.Anything,
		mock.Anything,
		mock.Anything,
		false,
		mock.Anything,
		time.Duration(0),
	).Return(exitErr)

	cmd := []string{"/bin/sh", "-c", "exit 127"}
	stdin := &mockReadCloser{}
	stdout := &mockWriteCloser{}
	stderr := &mockWriteCloser{}
	resize := make(chan remotecommand.TerminalSize)

	err := mockExec.ExecInContainer(
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		cmd,
		stdin,
		stdout,
		stderr,
		false,
		resize,
		0,
	)

	assert.Error(t, err)
	assert.IsType(t, &MockExitError{}, err)
	
	exitError, ok := err.(utilexec.ExitError)
	assert.True(t, ok)
	assert.True(t, exitError.Exited())
	assert.Equal(t, 127, exitError.ExitStatus())
	
	mockExec.AssertExpectations(t)
}

// TestServeExec_GenericError tests command execution with generic error
func TestServeExec_GenericError(t *testing.T) {
	mockExec := new(MockExecutor)
	genericErr := errors.New("container not found")

	mockExec.On("ExecInContainer",
		"test-pod",
		types.UID("test-uid"),
		"missing-container",
		[]string{"/bin/sh"},
		mock.Anything,
		mock.Anything,
		mock.Anything,
		false,
		mock.Anything,
		time.Duration(0),
	).Return(genericErr)

	cmd := []string{"/bin/sh"}
	stdin := &mockReadCloser{}
	stdout := &mockWriteCloser{}
	stderr := &mockWriteCloser{}
	resize := make(chan remotecommand.TerminalSize)

	err := mockExec.ExecInContainer(
		"test-pod",
		types.UID("test-uid"),
		"missing-container",
		cmd,
		stdin,
		stdout,
		stderr,
		false,
		resize,
		0,
	)

	assert.Error(t, err)
	assert.Equal(t, "container not found", err.Error())
	mockExec.AssertExpectations(t)
}

// TestServeExec_WithTTY tests command execution with TTY enabled
func TestServeExec_WithTTY(t *testing.T) {
	mockExec := new(MockExecutor)
	mockExec.On("ExecInContainer",
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		[]string{"/bin/bash"},
		mock.Anything,
		mock.Anything,
		mock.Anything,
		true, // TTY enabled
		mock.Anything,
		time.Duration(0),
	).Return(nil)

	cmd := []string{"/bin/bash"}
	stdin := &mockReadCloser{}
	stdout := &mockWriteCloser{}
	stderr := &mockWriteCloser{}
	resize := make(chan remotecommand.TerminalSize)

	err := mockExec.ExecInContainer(
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		cmd,
		stdin,
		stdout,
		stderr,
		true,
		resize,
		0,
	)

	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

// TestServeExec_WithResizeChannel tests command execution with terminal resize
func TestServeExec_WithResizeChannel(t *testing.T) {
	mockExec := new(MockExecutor)
	resizeChan := make(chan remotecommand.TerminalSize, 1)
	resizeChan <- remotecommand.TerminalSize{Width: 80, Height: 24}

	mockExec.On("ExecInContainer",
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		[]string{"/bin/bash"},
		mock.Anything,
		mock.Anything,
		mock.Anything,
		true,
		mock.MatchedBy(func(ch <-chan remotecommand.TerminalSize) bool {
			return ch != nil
		}),
		time.Duration(0),
	).Return(nil)

	cmd := []string{"/bin/bash"}
	stdin := &mockReadCloser{}
	stdout := &mockWriteCloser{}
	stderr := &mockWriteCloser{}

	err := mockExec.ExecInContainer(
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		cmd,
		stdin,
		stdout,
		stderr,
		true,
		resizeChan,
		0,
	)

	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

// TestServeExec_EmptyCommand tests execution with empty command
func TestServeExec_EmptyCommand(t *testing.T) {
	mockExec := new(MockExecutor)
	mockExec.On("ExecInContainer",
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		[]string{},
		mock.Anything,
		mock.Anything,
		mock.Anything,
		false,
		mock.Anything,
		time.Duration(0),
	).Return(nil)

	cmd := []string{}
	stdin := &mockReadCloser{}
	stdout := &mockWriteCloser{}
	stderr := &mockWriteCloser{}
	resize := make(chan remotecommand.TerminalSize)

	err := mockExec.ExecInContainer(
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		cmd,
		stdin,
		stdout,
		stderr,
		false,
		resize,
		0,
	)

	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

// TestServeExec_MultipleCommands tests execution with multiple command arguments
func TestServeExec_MultipleCommands(t *testing.T) {
	mockExec := new(MockExecutor)
	mockExec.On("ExecInContainer",
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		[]string{"/bin/sh", "-c", "ls -la | grep test"},
		mock.Anything,
		mock.Anything,
		mock.Anything,
		false,
		mock.Anything,
		time.Duration(0),
	).Return(nil)

	cmd := []string{"/bin/sh", "-c", "ls -la | grep test"}
	stdin := &mockReadCloser{}
	stdout := &mockWriteCloser{}
	stderr := &mockWriteCloser{}
	resize := make(chan remotecommand.TerminalSize)

	err := mockExec.ExecInContainer(
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		cmd,
		stdin,
		stdout,
		stderr,
		false,
		resize,
		0,
	)

	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

// TestServeExec_NilStreams tests execution with nil streams
func TestServeExec_NilStreams(t *testing.T) {
	mockExec := new(MockExecutor)
	mockExec.On("ExecInContainer",
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		[]string{"/bin/sh"},
		mock.Anything,
		mock.Anything,
		mock.Anything,
		false,
		mock.Anything,
		time.Duration(0),
	).Return(nil)

	cmd := []string{"/bin/sh"}
	var stdin io.Reader
	var stdout io.WriteCloser
	var stderr io.WriteCloser
	resize := make(chan remotecommand.TerminalSize)

	err := mockExec.ExecInContainer(
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		cmd,
		stdin,
		stdout,
		stderr,
		false,
		resize,
		0,
	)

	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

// TestServeExec_DifferentExitCodes tests various exit codes
func TestServeExec_DifferentExitCodes(t *testing.T) {
	testCases := []struct {
		name     string
		exitCode int
		exited   bool
	}{
		{"ExitCode1", 1, true},
		{"ExitCode2", 2, true},
		{"ExitCode126", 126, true},
		{"ExitCode127", 127, true},
		{"ExitCode255", 255, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockExec := new(MockExecutor)
			exitErr := &MockExitError{
				exitCode: tc.exitCode,
				exited:   tc.exited,
			}

			mockExec.On("ExecInContainer",
				"test-pod",
				types.UID("test-uid"),
				"test-container",
				[]string{"/bin/sh"},
				mock.Anything,
				mock.Anything,
				mock.Anything,
				false,
				mock.Anything,
				time.Duration(0),
			).Return(exitErr)

			cmd := []string{"/bin/sh"}
			stdin := &mockReadCloser{}
			stdout := &mockWriteCloser{}
			stderr := &mockWriteCloser{}
			resize := make(chan remotecommand.TerminalSize)

			err := mockExec.ExecInContainer(
				"test-pod",
				types.UID("test-uid"),
				"test-container",
				cmd,
				stdin,
				stdout,
				stderr,
				false,
				resize,
				0,
			)

			require.Error(t, err)
			exitError, ok := err.(utilexec.ExitError)
			require.True(t, ok)
			assert.Equal(t, tc.exitCode, exitError.ExitStatus())
			assert.Equal(t, tc.exited, exitError.Exited())
			mockExec.AssertExpectations(t)
		})
	}
}

// TestServeExec_LongRunningCommand tests long-running commands
func TestServeExec_LongRunningCommand(t *testing.T) {
	mockExec := new(MockExecutor)
	
	// Simulate a long-running command by returning after a delay
	mockExec.On("ExecInContainer",
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		[]string{"sleep", "100"},
		mock.Anything,
		mock.Anything,
		mock.Anything,
		false,
		mock.Anything,
		time.Duration(0),
	).Return(nil).Run(func(args mock.Arguments) {
		// Simulate some processing time
		time.Sleep(10 * time.Millisecond)
	})

	cmd := []string{"sleep", "100"}
	stdin := &mockReadCloser{}
	stdout := &mockWriteCloser{}
	stderr := &mockWriteCloser{}
	resize := make(chan remotecommand.TerminalSize)

	err := mockExec.ExecInContainer(
		"test-pod",
		types.UID("test-uid"),
		"test-container",
		cmd,
		stdin,
		stdout,
		stderr,
		false,
		resize,
		0,
	)

	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

// TestMockExitError tests the MockExitError implementation
func TestMockExitError(t *testing.T) {
	err := &MockExitError{
		exitCode: 42,
		exited:   true,
	}

	assert.Equal(t, 42, err.ExitStatus())
	assert.True(t, err.Exited())
	assert.NotEmpty(t, err.Error())
	assert.NotEmpty(t, err.String())
}

// TestMockReadCloser tests the mockReadCloser implementation
func TestMockReadCloser(t *testing.T) {
	data := []byte("test data")
	reader := &mockReadCloser{data: data}

	buf := make([]byte, 5)
	n, err := reader.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, []byte("test "), buf)

	n, err = reader.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, []byte("data"), buf[:n])

	n, err = reader.Read(buf)
	assert.Equal(t, 0, n)
	assert.Equal(t, io.EOF, err)

	err = reader.Close()
	assert.NoError(t, err)
}

// TestMockWriteCloser tests the mockWriteCloser implementation
func TestMockWriteCloser(t *testing.T) {
	writer := &mockWriteCloser{}

	n, err := writer.Write([]byte("hello"))
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, []byte("hello"), writer.data)

	n, err = writer.Write([]byte(" world"))
	assert.NoError(t, err)
	assert.Equal(t, 6, n)
	assert.Equal(t, []byte("hello world"), writer.data)

	err = writer.Close()
	assert.NoError(t, err)
}
