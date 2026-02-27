package logger

import (
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var Logger *zap.Logger
var Sugar *zap.SugaredLogger
var pid = os.Getpid()
var tuiHook *TUIHook

func init() {
	var err error
	Logger, err = NewLogger()
	if err != nil {
		panic("failed to initialize logger: " + err.Error())
	}
	Sugar = Logger.Sugar()
}

// natsStyleTimeEncoder formats time with PID prefix like NATS: [12345] 2026/01/10 20:56:44.509687
func natsStyleTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(fmt.Sprintf("[%d] %s", pid, t.Format("2006/01/02 15:04:05.000000")))
}

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
)

// natsStyleLevelEncoder formats level in brackets with colors: [INF] green, [WRN] yellow, [ERR] red
func natsStyleLevelEncoder(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	switch l {
	case zapcore.DebugLevel:
		enc.AppendString(colorCyan + "[DBG]" + colorReset)
	case zapcore.InfoLevel:
		enc.AppendString(colorGreen + "[INF]" + colorReset)
	case zapcore.WarnLevel:
		enc.AppendString(colorYellow + "[WRN]" + colorReset)
	case zapcore.ErrorLevel:
		enc.AppendString(colorRed + "[ERR]" + colorReset)
	case zapcore.FatalLevel:
		enc.AppendString(colorRed + "[FTL]" + colorReset)
	default:
		enc.AppendString(fmt.Sprintf("[%s]", l.CapitalString()[:3]))
	}
}

func NewLogger() (*zap.Logger, error) {
	config := zap.Config{
		Level:    zap.NewAtomicLevelAt(getLogLevel()),
		Encoding: "console",
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:          "time",
			LevelKey:         "level",
			NameKey:          "logger",
			CallerKey:        "",
			FunctionKey:      zapcore.OmitKey,
			MessageKey:       "msg",
			StacktraceKey:    "stacktrace",
			LineEnding:       zapcore.DefaultLineEnding,
			EncodeLevel:      natsStyleLevelEncoder,
			EncodeTime:       natsStyleTimeEncoder,
			EncodeDuration:   zapcore.SecondsDurationEncoder,
			ConsoleSeparator: " ",
		},
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}

	logger, err := config.Build()
	if err != nil {
		return nil, err
	}

	return logger.WithOptions(zap.AddCallerSkip(1)), nil
}

func getLogLevel() zapcore.Level {
	switch os.Getenv("LOG_LEVEL") {
	case "DEBUG":
		return zap.DebugLevel
	case "INFO":
		return zap.InfoLevel
	case "WARN":
		return zap.WarnLevel
	case "ERROR":
		return zap.ErrorLevel
	default:
		return zap.InfoLevel
	}
}

// Structured Logger functions (performance critical)
func Info(msg string, fields ...zap.Field) {
	Logger.Info(msg, fields...)
}

func Debug(msg string, fields ...zap.Field) {
	Logger.Debug(msg, fields...)
}

func Warn(msg string, fields ...zap.Field) {
	Logger.Warn(msg, fields...)
}

func Error(msg string, fields ...zap.Field) {
	Logger.Error(msg, fields...)
}

func Fatal(msg string, fields ...zap.Field) {
	Logger.Fatal(msg, fields...)
}

// SugaredLogger functions (more convenient, less performance critical)
func Infof(template string, args ...interface{}) {
	Sugar.Infof(template, args...)
}

func Infow(msg string, keysAndValues ...interface{}) {
	Sugar.Infow(msg, keysAndValues...)
}

func Debugf(template string, args ...interface{}) {
	Sugar.Debugf(template, args...)
}

func Debugw(msg string, keysAndValues ...interface{}) {
	Sugar.Debugw(msg, keysAndValues...)
}

func Warnf(template string, args ...interface{}) {
	Sugar.Warnf(template, args...)
}

func Warnw(msg string, keysAndValues ...interface{}) {
	Sugar.Warnw(msg, keysAndValues...)
}

func Errorf(template string, args ...interface{}) {
	Sugar.Errorf(template, args...)
}

func Errorw(msg string, keysAndValues ...interface{}) {
	Sugar.Errorw(msg, keysAndValues...)
}

func Fatalf(template string, args ...interface{}) {
	Sugar.Fatalf(template, args...)
}

func Fatalw(msg string, keysAndValues ...interface{}) {
	Sugar.Fatalw(msg, keysAndValues...)
}

func Sync() error {
	return Logger.Sync()
}

// AttachTUIHook attaches a hook for TUI log display
func AttachTUIHook(hook *TUIHook) error {
	newLogger, err := newLoggerWithHook(hook)
	if err != nil {
		return err
	}

	tuiHook = hook
	Logger = newLogger
	Sugar = Logger.Sugar()
	return nil
}

// DetachTUIHook removes the TUI hook and restores normal logging
func DetachTUIHook() {
	if tuiHook != nil {
		tuiHook.Close()
		tuiHook = nil
	}

	var err error
	Logger, err = NewLogger()
	if err != nil {
		Logger, _ = zap.NewProduction()
	}
	Sugar = Logger.Sugar()
}

// newLoggerWithHook creates a logger that also sends entries to the TUI hook
func newLoggerWithHook(hook *TUIHook) (*zap.Logger, error) {
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:          "time",
		LevelKey:         "level",
		NameKey:          "logger",
		CallerKey:        "",
		FunctionKey:      zapcore.OmitKey,
		MessageKey:       "msg",
		StacktraceKey:    "stacktrace",
		LineEnding:       zapcore.DefaultLineEnding,
		EncodeLevel:      natsStyleLevelEncoder,
		EncodeTime:       natsStyleTimeEncoder,
		EncodeDuration:   zapcore.SecondsDurationEncoder,
		ConsoleSeparator: " ",
	}

	encoder := zapcore.NewConsoleEncoder(encoderConfig)
	writer := zapcore.AddSync(os.Stdout)
	baseCore := zapcore.NewCore(encoder, writer, getLogLevel())
	wrappedCore := newTUICore(baseCore, hook)
	logger := zap.New(wrappedCore, zap.AddCallerSkip(1))

	return logger, nil
}

// GetTUIHook returns the current TUI hook if attached
func GetTUIHook() *TUIHook {
	return tuiHook
}
