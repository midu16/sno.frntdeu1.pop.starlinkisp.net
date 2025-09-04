package logger

import (
	"io"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
)

// Logger wraps logrus.Logger with additional functionality
type Logger struct {
	*logrus.Logger
	logFile *os.File
}

// NewLogger creates a new logger instance
func NewLogger() *Logger {
	logger := logrus.New()
	
	// Set log format
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
		ForceColors:     true,
	})

	// Set log level
	logger.SetLevel(logrus.InfoLevel)

	// Create log file
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		logger.Warnf("Failed to create log directory: %v", err)
	}

	logFile, err := os.OpenFile(
		filepath.Join(logDir, "openshift_sno_hub_install.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0666,
	)
	if err != nil {
		logger.Warnf("Failed to open log file: %v", err)
	} else {
		// Write to both stdout and file
		multiWriter := io.MultiWriter(os.Stdout, logFile)
		logger.SetOutput(multiWriter)
	}

	return &Logger{
		Logger:  logger,
		logFile: logFile,
	}
}

// Close closes the log file
func (l *Logger) Close() error {
	if l.logFile != nil {
		return l.logFile.Close()
	}
	return nil
}

// LogWithLevel logs a message with the specified level and color
func (l *Logger) LogWithLevel(level logrus.Level, message string, args ...interface{}) {
	switch level {
	case logrus.InfoLevel:
		l.Infof(message, args...)
	case logrus.WarnLevel:
		l.Warnf(message, args...)
	case logrus.ErrorLevel:
		l.Errorf(message, args...)
	case logrus.DebugLevel:
		l.Debugf(message, args...)
	default:
		l.Infof(message, args...)
	}
}

// LogInfo logs an info message
func (l *Logger) LogInfo(message string, args ...interface{}) {
	l.LogWithLevel(logrus.InfoLevel, message, args...)
}

// LogWarn logs a warning message
func (l *Logger) LogWarn(message string, args ...interface{}) {
	l.LogWithLevel(logrus.WarnLevel, message, args...)
}

// LogError logs an error message
func (l *Logger) LogError(message string, args ...interface{}) {
	l.LogWithLevel(logrus.ErrorLevel, message, args...)
}

// LogSuccess logs a success message
func (l *Logger) LogSuccess(message string, args ...interface{}) {
	l.WithField("status", "SUCCESS").Infof(message, args...)
}

// LogDebug logs a debug message
func (l *Logger) LogDebug(message string, args ...interface{}) {
	l.LogWithLevel(logrus.DebugLevel, message, args...)
}