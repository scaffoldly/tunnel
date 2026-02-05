package logger

// Logging configuration - simplified for console only
type Config struct {
	ConsoleConfig *ConsoleConfig
	MinLevel      string // debug | info | error | fatal
}

type ConsoleConfig struct {
	noColor bool
	asJSON  bool
}

func CreateConfig(
	minLevel string,
	disableTerminal bool,
	formatJSON bool,
	_, _ string, // rollingLogPath and nonRollingLogFilePath ignored
) *Config {
	var console *ConsoleConfig
	if !disableTerminal {
		console = &ConsoleConfig{
			noColor: false,
			asJSON:  formatJSON,
		}
	}

	if minLevel == "" {
		minLevel = "info"
	}

	return &Config{
		ConsoleConfig: console,
		MinLevel:      minLevel,
	}
}
