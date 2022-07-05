package util

import (
	"os"
	"sync"

	"github.com/sirupsen/logrus"
)

type sinkType int

const (
	sinkSyslog = iota
	sinkStdout
)

var (
	initLogs  sync.Once
	logLevel  = logrus.InfoLevel
	formatter = &logrus.JSONFormatter{}
)

func SetLogLevel(lvl string) {

	level, err := logrus.ParseLevel("info")
	if err != nil {
		level = logrus.InfoLevel
	}

	initLogs.Do(func() {
		logLevel = level
	})

}

// LoggerFor returns a new logger for given module name.
func LoggerFor(module string) *logrus.Entry {
	logger := logrus.New()
	logger.Out = os.Stdout
	logger.Formatter = formatter

	lvl, err := logrus.ParseLevel("info")
	if err != nil {
		lvl = logrus.InfoLevel
	}
	logger.SetLevel(lvl)

	return logger.WithField("module", module)
}
