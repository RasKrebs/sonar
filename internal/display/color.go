package display

import (
	"os"
	"strings"
)

// ANSI escape codes
const (
	reset     = "\033[0m"
	bold      = "\033[1m"
	underline = "\033[4m"
	dim       = "\033[2m"

	red     = "\033[31m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	blue    = "\033[34m"
	magenta = "\033[35m"
	cyan    = "\033[36m"
	white   = "\033[37m"
)

// NoColor tracks whether color output is disabled.
var NoColor bool

func init() {
	// Respect NO_COLOR convention (https://no-color.org)
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		NoColor = true
		return
	}

	// Detect if stdout is a pipe/not a terminal
	fi, err := os.Stdout.Stat()
	if err == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		NoColor = true
		return
	}

	// Check TERM
	term := os.Getenv("TERM")
	if term == "dumb" || term == "" {
		NoColor = true
	}
}

func colorize(s string, codes ...string) string {
	if NoColor {
		return s
	}
	return strings.Join(codes, "") + s + reset
}

func Bold(s string) string      { return colorize(s, bold) }
func Dim(s string) string       { return colorize(s, dim) }
func Red(s string) string       { return colorize(s, red) }
func Green(s string) string     { return colorize(s, green) }
func Yellow(s string) string    { return colorize(s, yellow) }
func Blue(s string) string      { return colorize(s, blue) }
func Magenta(s string) string   { return colorize(s, magenta) }
func Cyan(s string) string      { return colorize(s, cyan) }
func BoldCyan(s string) string  { return colorize(s, bold, cyan) }
func Underline(s string) string { return colorize(s, underline, blue) }
