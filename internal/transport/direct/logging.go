package direct

import "github.com/pion/logging"

// SDK logging is unconditionally disabled, including PION_LOG_* environment
// overrides. Application diagnostics contain aggregate counters/reasons only.
type silentLogger struct{}

func (silentLogger) NewLogger(string) logging.LeveledLogger { return silentLogger{} }
func (silentLogger) Trace(string)                           {}
func (silentLogger) Tracef(string, ...any)                  {}
func (silentLogger) Debug(string)                           {}
func (silentLogger) Debugf(string, ...any)                  {}
func (silentLogger) Info(string)                            {}
func (silentLogger) Infof(string, ...any)                   {}
func (silentLogger) Warn(string)                            {}
func (silentLogger) Warnf(string, ...any)                   {}
func (silentLogger) Error(string)                           {}
func (silentLogger) Errorf(string, ...any)                  {}
