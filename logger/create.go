package logger

import (
	"io"
	"os"
	"time"

	"github.com/mattn/go-colorable"
	"github.com/rs/zerolog"
	"github.com/urfave/cli/v2"
	"golang.org/x/term"

	cfdflags "github.com/cloudflare/cloudflared/cmd/cloudflared/flags"
	"github.com/cloudflare/cloudflared/management"
)

const (
	EnableTerminalLog  = false
	DisableTerminalLog = true

	consoleTimeFormat = time.RFC3339
)

var (
	ManagementLogger *management.Logger
)

func init() {
	zerolog.TimeFieldFormat = time.RFC3339
	zerolog.TimestampFunc = utcNow

	ManagementLogger = management.NewLogger()
}

func utcNow() time.Time {
	return time.Now().UTC()
}

// resilientMultiWriter is an alternative to zerolog's so that we can make it resilient to individual
// writer's errors.
type resilientMultiWriter struct {
	level            zerolog.Level
	writers          []io.Writer
	managementWriter zerolog.LevelWriter
}

func (t resilientMultiWriter) Write(p []byte) (n int, err error) {
	for _, w := range t.writers {
		_, _ = w.Write(p)
	}
	if t.managementWriter != nil {
		_, _ = t.managementWriter.Write(p)
	}
	return len(p), nil
}

func (t resilientMultiWriter) WriteLevel(level zerolog.Level, p []byte) (n int, err error) {
	// Only write the event to normal writers if it exceeds the level, but always write to the
	// management logger and let it decided with the provided level of the log event.
	if t.level <= level {
		for _, w := range t.writers {
			_, _ = w.Write(p)
		}
	}
	if t.managementWriter != nil {
		_, _ = t.managementWriter.WriteLevel(level, p)
	}
	return len(p), nil
}

var levelErrorLogged = false

func newZerolog(loggerConfig *Config) *zerolog.Logger {
	var writers []io.Writer

	if loggerConfig.ConsoleConfig != nil {
		writers = append(writers, createConsoleLogger(*loggerConfig.ConsoleConfig))
	}

	managementWriter := ManagementLogger

	level, levelErr := zerolog.ParseLevel(loggerConfig.MinLevel)
	if levelErr != nil {
		level = zerolog.InfoLevel
	}

	multi := resilientMultiWriter{level, writers, managementWriter}
	log := zerolog.New(multi).With().Timestamp().Logger()
	if !levelErrorLogged && levelErr != nil {
		log.Error().Msgf("Failed to parse log level %q, using %q instead", loggerConfig.MinLevel, level)
		levelErrorLogged = true
	}

	return &log
}

func CreateTransportLoggerFromContext(c *cli.Context, disableTerminal bool) *zerolog.Logger {
	return createFromContext(c, cfdflags.TransportLogLevel, disableTerminal)
}

func CreateLoggerFromContext(c *cli.Context, disableTerminal bool) *zerolog.Logger {
	return createFromContext(c, cfdflags.LogLevel, disableTerminal)
}

func createFromContext(
	c *cli.Context,
	logLevelFlagName string,
	disableTerminal bool,
) *zerolog.Logger {
	logLevel := c.String(logLevelFlagName)
	var logFormatJSON bool
	switch c.String(cfdflags.LogFormatOutput) {
	case cfdflags.LogFormatOutputValueJSON:
		logFormatJSON = true
	default:
		logFormatJSON = false
	}

	loggerConfig := CreateConfig(
		logLevel,
		disableTerminal,
		logFormatJSON,
		"", "", // no file logging
	)

	return newZerolog(loggerConfig)
}

func createConsoleLogger(config ConsoleConfig) io.Writer {
	if config.asJSON {
		return &consoleWriter{out: os.Stderr}
	}
	consoleOut := os.Stderr
	return zerolog.ConsoleWriter{
		Out:        colorable.NewColorable(consoleOut),
		NoColor:    config.noColor || !term.IsTerminal(int(consoleOut.Fd())),
		TimeFormat: consoleTimeFormat,
	}
}
