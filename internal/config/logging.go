package config

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

const (
	levelNotSet       = slog.Level(-100)
	levelCritical     = slog.Level(12)
	defaultLoggerName = "jobsniper"
	logTimeLayout     = "2006-01-02 15:04:05"
)

// ConfigureLogging installs the default slog logger with Python-like text
// formatting: records below WARN go to stdout and WARN or above go to stderr.
func ConfigureLogging(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("configure logging: %w", err)
	}

	level := ParseLogLevel(valueOr(os.LookupEnv, "LOG_LEVEL", "INFO"))
	slog.SetDefault(slog.New(NewHandler(os.Stdout, os.Stderr, level, defaultLoggerName)))
	return nil
}

// ParseLogLevel resolves LOG_LEVEL the way Python's getattr(logging, ...)
// does, falling back to INFO for unknown or missing values.
func ParseLogLevel(raw string) slog.Level {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO", "":
		return slog.LevelInfo
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	case "CRITICAL", "FATAL":
		return levelCritical
	case "NOTSET":
		return levelNotSet
	default:
		return slog.LevelInfo
	}
}

// NewHandler returns a slog handler that writes below-WARN records to stdout
// and WARN-or-above records to stderr.
func NewHandler(stdout, stderr io.Writer, minLevel slog.Level, loggerName string) slog.Handler {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if loggerName == "" {
		loggerName = defaultLoggerName
	}
	return &splitHandler{
		mu:       &sync.Mutex{},
		stdout:   stdout,
		stderr:   stderr,
		minLevel: minLevel,
		name:     loggerName,
	}
}

type splitHandler struct {
	mu       *sync.Mutex
	stdout   io.Writer
	stderr   io.Writer
	minLevel slog.Level
	name     string
	prefix   string
	attrs    []slog.Attr
}

func (h *splitHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minLevel
}

func (h *splitHandler) Handle(_ context.Context, record slog.Record) error {
	if !h.Enabled(context.Background(), record.Level) {
		return nil
	}

	line := h.format(record)
	writer := h.stdout
	if record.Level >= slog.LevelWarn {
		writer = h.stderr
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(writer, line)
	return err
}

func (h *splitHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	clone := h.clone()
	for _, attr := range attrs {
		clone.attrs = append(clone.attrs, clone.prefixAttribute(attr))
	}
	return clone
}

func (h *splitHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	clone := h.clone()
	if clone.prefix == "" {
		clone.prefix = name
	} else {
		clone.prefix += "." + name
	}
	return clone
}

func (h *splitHandler) clone() *splitHandler {
	clone := *h
	clone.attrs = append([]slog.Attr(nil), h.attrs...)
	return &clone
}

func (h *splitHandler) prefixAttribute(attr slog.Attr) slog.Attr {
	if h.prefix != "" {
		attr.Key = h.prefix + "." + attr.Key
	}
	return attr
}

func (h *splitHandler) format(record slog.Record) string {
	name := h.name
	attrs := make([]slog.Attr, 0, len(h.attrs)+record.NumAttrs())
	for _, attr := range h.attrs {
		appendLogAttribute(&name, &attrs, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		appendLogAttribute(&name, &attrs, h.prefixAttribute(attr))
		return true
	})

	var builder strings.Builder
	builder.WriteString(record.Time.Format(logTimeLayout))
	builder.WriteByte(' ')
	builder.WriteString(fmt.Sprintf("%-8s", levelName(record.Level)))
	builder.WriteByte(' ')
	builder.WriteString(name)
	builder.WriteString(": ")
	builder.WriteString(record.Message)
	for _, attr := range attrs {
		builder.WriteByte(' ')
		builder.WriteString(formatLogAttribute(attr))
	}
	builder.WriteByte('\n')
	return builder.String()
}

func appendLogAttribute(name *string, attrs *[]slog.Attr, attr slog.Attr) {
	if attr.Key == "name" {
		if value := attr.Value.Resolve(); value.Kind() == slog.KindString {
			*name = value.String()
			return
		}
	}
	*attrs = append(*attrs, attr)
}

func formatLogAttribute(attr slog.Attr) string {
	value := attr.Value.Resolve()
	if value.Kind() != slog.KindGroup {
		return attr.Key + "=" + value.String()
	}

	parts := make([]string, 0, len(value.Group()))
	for _, nested := range value.Group() {
		key := nested.Key
		if attr.Key != "" {
			key = attr.Key + "." + key
		}
		parts = append(parts, formatLogAttribute(slog.Attr{Key: key, Value: nested.Value}))
	}
	return strings.Join(parts, " ")
}

func levelName(level slog.Level) string {
	switch {
	case level >= levelCritical:
		return "CRITICAL"
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARNING"
	case level >= slog.LevelInfo:
		return "INFO"
	case level >= slog.LevelDebug:
		return "DEBUG"
	case level == levelNotSet:
		return "NOTSET"
	default:
		return level.String()
	}
}
