// Package logging 基于 uber-go/zap 提供统一的结构化日志能力
//
// 使用方式：
//
//	// main.go 中初始化
//	logging.Init(logging.Config{Level: "info", Format: "console", Output: "stderr"})
//
//	// 各包获取具名 logger（Init 前后均可安全调用）
//	var logger = logging.New("sfu")
//	logger.Infow("房间已创建", "roomID", roomID)
//	logger.Warnw("连接失败", "error", err)
//
// 可配置项通过 config.yaml 或环境变量控制：
//
//	log.level:  debug | info | warn | error       （默认 info）
//	log.format: console | json                    （默认 console）
//	log.output: stderr | stdout | <目录路径>      （默认 stderr）
//
// output 为目录路径时启用按天切分：
//   - 每天一个文件：echat-2026-07-26.log
//   - 跨天自动切换到新文件
//   - 保留最近 30 天，超期自动删除
//   - 目录不存在时自动创建
package logging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config 日志初始化配置，由 config 包传入
type Config struct {
	Level         string // debug | info | warn | error，默认 "info"
	Format        string // console | json，默认 "console"
	EnableConsole bool   // 输出到终端 stderr，默认 true
	EnableFile    bool   // 输出到按天日志文件，默认 true
	FileDir       string // 日志文件目录，默认 "logs"
}

// ---------- Logger 包装器 ----------

// Logger 始终代理到当前全局 zap logger 的轻量包装
//
// 支持在 Init() 之前创建（如包级 var logger = logging.New("sfu")），
// 每次日志调用动态解析当前全局 logger，确保 Init() 更新输出目标后立即生效
type Logger struct {
	module string
}

// L 返回无模块名的根 logger
func L() *Logger {
	return &Logger{}
}

// New 创建带 module 字段的具名 logger
// logging.New("sfu") 输出的每条日志都会携带 "module": "sfu"
func New(module string) *Logger {
	return &Logger{module: module}
}

// sugared 返回当前全局 logger 带模块名的 sugared 实例
func (l *Logger) sugared() *zap.SugaredLogger {
	g := getGlobal()
	if l.module == "" {
		return g
	}
	return g.With("module", l.module)
}

// Infow 结构化 Info 级别日志
func (l *Logger) Infow(msg string, keysAndValues ...interface{}) {
	l.sugared().Infow(msg, keysAndValues...)
}

// Warnw 结构化 Warn 级别日志
func (l *Logger) Warnw(msg string, keysAndValues ...interface{}) {
	l.sugared().Warnw(msg, keysAndValues...)
}

// Debugw 结构化 Debug 级别日志
func (l *Logger) Debugw(msg string, keysAndValues ...interface{}) {
	l.sugared().Debugw(msg, keysAndValues...)
}

// Fatalw 结构化 Fatal 级别日志，输出后调用 os.Exit(1)
func (l *Logger) Fatalw(msg string, keysAndValues ...interface{}) {
	l.sugared().Fatalw(msg, keysAndValues...)
}

// ---------- 全局状态 ----------

var (
	globalMu sync.RWMutex
	global   *zap.SugaredLogger // Init() 成功后设置，nil 表示未初始化
	flushFn  func()             // 供 Sync 调用，保证退出前日志落盘
)

// Init 初始化全局 logger，必须在 config.Load() 之后调用
// 多次调用会替换已有 logger
func Init(cfg Config) {
	l, f := buildLogger(cfg)

	globalMu.Lock()
	global = l
	flushFn = f
	globalMu.Unlock()
}

// Sync 刷新缓冲区，确保所有日志已落盘
// 应在程序退出前调用（通常在 main 中 defer logging.Sync()）
func Sync() {
	globalMu.RLock()
	f := flushFn
	globalMu.RUnlock()
	if f != nil {
		f()
	}
}

// getGlobal 返回当前全局 logger，未初始化时返回 stderr 兜底 logger
func getGlobal() *zap.SugaredLogger {
	globalMu.RLock()
	g := global
	globalMu.RUnlock()
	if g != nil {
		return g
	}
	// 兜底：Init 未调用时使用 stderr 输出，确保不 panic
	fallback, _ := buildLogger(Config{Level: "debug", Format: "console", EnableConsole: true})
	return fallback
}

// ---------- 构建 ----------

// buildLogger 根据配置构建 zap.Logger 并返回 sugared 版本
func buildLogger(cfg Config) (*zap.SugaredLogger, func()) {
	level := parseLevel(cfg.Level)

	var cores []zapcore.Core
	var closers []func()

	// 终端输出
	if cfg.EnableConsole {
		encoder := newEncoder(cfg.Format, false) // 带 ANSI 颜色
		cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(os.Stderr), level))
		closers = append(closers, func() { os.Stderr.Sync() })
	}

	// 文件输出
	if cfg.EnableFile {
		dir := cfg.FileDir
		if dir == "" {
			dir = "logs"
		}
		dr := newDailyRotator(dir, 30)
		encoder := newEncoder(cfg.Format, true) // 无颜色码
		cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(dr), level))
		closers = append(closers, func() { dr.Close() })
	}

	// 兜底：两者都关闭时仍输出到 stderr，确保日志不丢失
	if len(cores) == 0 {
		encoder := newEncoder(cfg.Format, false)
		cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(os.Stderr), level))
		closers = append(closers, func() { os.Stderr.Sync() })
	}

	var core zapcore.Core
	if len(cores) == 1 {
		core = cores[0]
	} else {
		core = zapcore.NewTee(cores...)
	}

	opts := encoderOptions(cfg.Format)
	logger := zap.New(core, opts...)
	return logger.Sugar(), func() {
		for _, c := range closers {
			c()
		}
	}
}

// newEncoder 根据格式创建编码器
// fileOutput 为 true 时，即使 console 格式也不输出 ANSI 颜色码
func newEncoder(format string, fileOutput bool) zapcore.Encoder {
	var encoderCfg zapcore.EncoderConfig

	if format == "json" {
		encoderCfg = zap.NewProductionEncoderConfig()
		encoderCfg.TimeKey = "ts"
		encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	} else {
		encoderCfg = zap.NewDevelopmentEncoderConfig()
		encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
		if fileOutput {
			encoderCfg.EncodeLevel = zapcore.CapitalLevelEncoder
		} else {
			encoderCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		}
	}

	return zapcore.NewConsoleEncoder(encoderCfg)
}

// encoderOptions 返回编码器相关的 zap Option
func encoderOptions(format string) []zap.Option {
	if format == "json" {
		return nil
	}
	return []zap.Option{zap.AddCaller(), zap.AddCallerSkip(1)}
}

// parseLevel 将配置字符串转为 zapcore.Level，不识别时默认 info
func parseLevel(s string) zapcore.Level {
	switch s {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// ---------- 按天切分写入器 ----------

// dailyRotator 实现按天切分的日志写入器
//
// 每当日志写入跨越午夜，自动关闭当天文件、创建新文件
// 文件命名：echat-2006-01-02.log
// 定期清理超过 maxAge 天的旧文件
type dailyRotator struct {
	dir    string // 日志目录
	maxAge int    // 保留天数
	mu     sync.Mutex
	file   *os.File
	day    string // 当前文件对应的日期 "2006-01-02"
}

// newDailyRotator 创建按天切分写入器，目录不存在时自动创建
func newDailyRotator(dir string, maxAge int) *dailyRotator {
	if err := os.MkdirAll(dir, 0755); err != nil {
		// 目录创建失败时回退到 stderr，确保日志不丢失
		os.Stderr.WriteString("[logging] 无法创建日志目录 " + dir + ": " + err.Error() + "\n")
	}
	return &dailyRotator{dir: dir, maxAge: maxAge}
}

// Write 实现 io.Writer，写入前检查日期是否需要切换文件
func (d *dailyRotator) Write(p []byte) (n int, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if today != d.day {
		d.rotate(today)
	}

	if d.file == nil {
		return len(p), nil // 文件未打开时静默丢弃，已在 rotate 中打印了错误
	}
	return d.file.Write(p)
}

// rotate 切换到新日期文件，关闭旧文件并清理过期文件
func (d *dailyRotator) rotate(today string) {
	if d.file != nil {
		d.file.Close()
	}
	d.file = nil
	d.day = ""

	f, err := os.OpenFile(
		filepath.Join(d.dir, "echat-"+today+".log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0644,
	)
	if err != nil {
		os.Stderr.WriteString("[logging] 无法创建日志文件 " + today + ".log: " + err.Error() + "\n")
		return
	}

	d.file = f
	d.day = today

	// 清理过期文件
	d.cleanup()
}

// cleanup 删除超过 maxAge 天的旧日志文件
func (d *dailyRotator) cleanup() {
	cutoff := time.Now().AddDate(0, 0, -d.maxAge)

	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}

	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "echat-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		// 提取日期：echat-2006-01-02.log → 2006-01-02
		dateStr := strings.TrimPrefix(name, "echat-")
		dateStr = strings.TrimSuffix(dateStr, ".log")
		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			os.Remove(filepath.Join(d.dir, name))
		}
	}
}

// Close 关闭当前日志文件
func (d *dailyRotator) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file != nil {
		err := d.file.Close()
		d.file = nil
		return err
	}
	return nil
}
