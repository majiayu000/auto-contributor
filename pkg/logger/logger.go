package logger

import (
	"os"
	"time"

	"github.com/sirupsen/logrus"
)

// Logger is the application logger instance
var log *logrus.Logger

func init() {
	log = logrus.New()
	log.SetOutput(os.Stdout)
	log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "15:04:05",
		ForceColors:     true,
	})
	log.SetLevel(logrus.InfoLevel)
}

// SetLevel sets the logging level
func SetLevel(level string) {
	switch level {
	case "debug":
		log.SetLevel(logrus.DebugLevel)
	case "info":
		log.SetLevel(logrus.InfoLevel)
	case "warn":
		log.SetLevel(logrus.WarnLevel)
	case "error":
		log.SetLevel(logrus.ErrorLevel)
	}
}

// SetJSON sets JSON format for logging
func SetJSON() {
	log.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339,
	})
}

// ComponentLogger provides structured logging for a component
type ComponentLogger struct {
	entry *logrus.Entry
}

// NewComponent creates a logger with a component name
func NewComponent(name string) *ComponentLogger {
	return &ComponentLogger{
		entry: log.WithField("component", name),
	}
}

// Debug logs a debug message with fields
func (c *ComponentLogger) Debug(msg string, keysAndValues ...interface{}) {
	c.entry.WithFields(parseFields(keysAndValues...)).Debug(msg)
}

// Info logs an info message with fields
func (c *ComponentLogger) Info(msg string, keysAndValues ...interface{}) {
	c.entry.WithFields(parseFields(keysAndValues...)).Info(msg)
}

// Warn logs a warning message with fields
func (c *ComponentLogger) Warn(msg string, keysAndValues ...interface{}) {
	c.entry.WithFields(parseFields(keysAndValues...)).Warn(msg)
}

// Error logs an error message with fields
func (c *ComponentLogger) Error(msg string, keysAndValues ...interface{}) {
	c.entry.WithFields(parseFields(keysAndValues...)).Error(msg)
}

// Fatal logs a fatal message and exits
func (c *ComponentLogger) Fatal(msg string, keysAndValues ...interface{}) {
	c.entry.WithFields(parseFields(keysAndValues...)).Fatal(msg)
}

// WithFields creates a new logger with additional fields
func (c *ComponentLogger) WithFields(fields logrus.Fields) *logrus.Entry {
	return c.entry.WithFields(fields)
}

// Package-level functions for backward compatibility

// Debug logs a debug message
func Debug(msg string, keysAndValues ...interface{}) {
	log.WithFields(parseFields(keysAndValues...)).Debug(msg)
}

// Info logs an info message
func Info(msg string, keysAndValues ...interface{}) {
	log.WithFields(parseFields(keysAndValues...)).Info(msg)
}

// Warn logs a warning message
func Warn(msg string, keysAndValues ...interface{}) {
	log.WithFields(parseFields(keysAndValues...)).Warn(msg)
}

// Error logs an error message
func Error(msg string, keysAndValues ...interface{}) {
	log.WithFields(parseFields(keysAndValues...)).Error(msg)
}

// Fatal logs a fatal message and exits
func Fatal(msg string, keysAndValues ...interface{}) {
	log.WithFields(parseFields(keysAndValues...)).Fatal(msg)
}

// parseFields converts key-value pairs to logrus.Fields
func parseFields(keysAndValues ...interface{}) logrus.Fields {
	f := logrus.Fields{}
	for i := 0; i < len(keysAndValues)-1; i += 2 {
		if key, ok := keysAndValues[i].(string); ok {
			f[key] = keysAndValues[i+1]
		}
	}
	return f
}
