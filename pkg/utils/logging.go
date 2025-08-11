package utils

import (
	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// LoggerConfig holds configuration for structured logging
type LoggerConfig struct {
	Development bool
	Level       int
}

// SetupLogger configures structured logging for the application
func SetupLogger(config LoggerConfig) logr.Logger {
	// The logger is already configured in main.go using zap
	// This function can be used for additional logger configuration if needed
	return ctrl.Log
}

// LogWithContext adds common context fields to a logger
func LogWithContext(log logr.Logger, component, operation string) logr.Logger {
	return log.WithValues(
		"component", component,
		"operation", operation,
	)
}

// LogError logs an error with additional context
func LogError(log logr.Logger, err error, msg string, keysAndValues ...interface{}) {
	log.Error(err, msg, keysAndValues...)
}

// LogInfo logs an info message with context
func LogInfo(log logr.Logger, msg string, keysAndValues ...interface{}) {
	log.Info(msg, keysAndValues...)
}
