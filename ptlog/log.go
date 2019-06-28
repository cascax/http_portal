package ptlog

import (
	"fmt"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
	"os"
	"time"
)

var pid = os.Getpid()

const (
	cReset   = 0
	cRed     = 31
	cGreen   = 32
	cYellow  = 33
	cMagenta = 35
)

type ZapLogger struct {
	*zap.SugaredLogger
	Atom   zap.AtomicLevel
	Logger *zap.Logger
	Writer *lumberjack.Logger
}

func colorizeLevelEncoder(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(fmt.Sprintf("|\x1b[%dm%s\x1b[0m|\t%d", levelColor(l), l.CapitalString(), pid))
}

func levelEncoder(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(fmt.Sprintf("|%s|\t%d", l.CapitalString(), pid))
}

func localTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("2006-01-02 15:04:05.000"))
}

func levelColor(level zapcore.Level) int {
	switch level {
	case zapcore.DebugLevel:
		return cMagenta
	case zapcore.InfoLevel:
		return cGreen
	case zapcore.WarnLevel:
		return cYellow
	case zapcore.ErrorLevel, zapcore.FatalLevel, zapcore.PanicLevel:
		return cRed
	default:
		return cReset
	}
}

func NewLog(conf LogConfig, options ...zap.Option) (*ZapLogger, error) {
	writer := &lumberjack.Logger{
		Filename:   conf.Filename(),
		MaxSize:    conf.MaxSize,
		MaxBackups: conf.MaxBackup,
		LocalTime:  true,
		Compress:   false,
	}
	_, err := writer.Write(make([]byte, 0, 1))
	if err != nil {
		return nil, err
	}

	atom := zap.NewAtomicLevel()
	atom.SetLevel(conf.ZapLevel())
	config := zap.NewProductionEncoderConfig()
	config.EncodeTime = localTimeEncoder
	config.EncodeLevel = levelEncoder
	core := zapcore.NewCore(zapcore.NewConsoleEncoder(config), zapcore.AddSync(writer), atom)

	options = append(options, zap.AddCaller())
	logger := zap.New(core, options...)
	sugar := logger.Sugar()
	p := &ZapLogger{sugar, atom, logger, writer}
	return p, nil
}

func NewConsoleLog(options ...zap.Option) *ZapLogger {
	atom := zap.NewAtomicLevel()
	atom.SetLevel(zapcore.DebugLevel)
	config := zap.NewDevelopmentEncoderConfig()
	config.EncodeTime = localTimeEncoder
	config.EncodeLevel = colorizeLevelEncoder
	core := zapcore.NewCore(zapcore.NewConsoleEncoder(config), os.Stdout, atom)

	options = append(options, zap.AddCaller())
	logger := zap.New(core, options...)
	sugar := logger.Sugar()
	return &ZapLogger{sugar, atom, logger, nil}
}
