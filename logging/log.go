package logging

import (
	"fmt"
	"io"
	"log"
	"os"
)

// ANSI Color Codes
const (
	Reset   = "\033[0m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Cyan    = "\033[36m"
	Magenta = "\033[35m"
	White   = "\033[97m"
)

type LogLevel int

const (
	LogLevelError   LogLevel = 0
	LogLevelWarning LogLevel = 1
	LogLevelInfo    LogLevel = 2
	LogLevelDebug   LogLevel = 3
)

var logLevel = LogLevelError // the default

func SetLogLevel(newLevel int) {
	logLevel = LogLevel(newLevel)
}

func SetLogFile(logFile io.Writer) {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	logOutput := io.MultiWriter(os.Stdout, logFile)
	log.SetOutput(logOutput)
}

func getPrefix(level string) string {
	return fmt.Sprintf("[%s]", level)
}

func Fatalln(args ...interface{}) {
	log.Fatalln(args...)
}

func Fatalf(format string, args ...interface{}) {
	log.Fatalf(format, args...)
}

func Fatal(args ...interface{}) {
	log.Fatal(args...)
}

func Debugf(format string, args ...interface{}) {
	if logLevel >= LogLevelDebug {
		log.Printf(Cyan+fmt.Sprintf("%s %s", getPrefix("DEBUG"), format)+Reset, args...)
	}
}

func Infof(format string, args ...interface{}) {
	if logLevel >= LogLevelInfo {
		log.Printf(White+fmt.Sprintf("%s %s", getPrefix("INFO"), format)+Reset, args...)
	}
}

// Successf prints in green, for successful events like finding a block.
func Successf(format string, args ...interface{}) {
	if logLevel >= LogLevelInfo {
		log.Printf(Green+fmt.Sprintf("%s %s", getPrefix("SUCCESS"), format)+Reset, args...)
	}
}

// Noticef prints in magenta, for noteworthy events like receiving new work.
func Noticef(format string, args ...interface{}) {
	if logLevel >= LogLevelInfo {
		log.Printf(Magenta+fmt.Sprintf("%s %s", getPrefix("NOTICE"), format)+Reset, args...)
	}
}

func Warnf(format string, args ...interface{}) {
	if logLevel >= LogLevelWarning {
		log.Printf(Yellow+fmt.Sprintf("%s %s", getPrefix("WARN"), format)+Reset, args...)
	}
}

func Errorf(format string, args ...interface{}) {
	if logLevel >= LogLevelError {
		log.Printf(Red+fmt.Sprintf("%s %s", getPrefix("ERROR"), format)+Reset, args...)
	}
}

func Debugln(args ...interface{}) {
	if logLevel >= LogLevelDebug {
		log.Println(append([]interface{}{Cyan + getPrefix("DEBUG")}, args...)...)
	}
}

func Infoln(args ...interface{}) {
	if logLevel >= LogLevelInfo {
		log.Println(append([]interface{}{White + getPrefix("INFO")}, args...)...)
	}
}

func Warnln(args ...interface{}) {
	if logLevel >= LogLevelWarning {
		log.Println(append([]interface{}{Yellow + getPrefix("WARN")}, args...)...)
	}
}

func Errorln(args ...interface{}) {
	if logLevel >= LogLevelError {
		log.Println(append([]interface{}{Red + getPrefix("ERROR")}, args...)...)
	}
}

func Debug(args ...interface{}) {
	if logLevel >= LogLevelDebug {
		log.Print(append([]interface{}{Cyan + getPrefix("DEBUG")}, args...)...)
	}
}

func Info(args ...interface{}) {
	if logLevel >= LogLevelInfo {
		log.Print(append([]interface{}{White + getPrefix("INFO")}, args...)...)
	}
}

func Warn(args ...interface{}) {
	if logLevel >= LogLevelWarning {
		log.Print(append([]interface{}{Yellow + getPrefix("WARN")}, args...)...)
	}
}

func Error(args ...interface{}) {
	if logLevel >= LogLevelError {
		log.Print(append([]interface{}{Red + getPrefix("ERROR")}, args...)...)
	}
}
