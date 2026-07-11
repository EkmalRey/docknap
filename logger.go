package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type LogLevel string

const (
	LevelDebug LogLevel = "debug"
	LevelInfo  LogLevel = "info"
	LevelWarn  LogLevel = "warn"
	LevelError LogLevel = "error"
)

type Logger struct {
	mu     sync.Mutex
	out    io.Writer
	json   bool
	fields map[string]any
}

var defaultLogger = NewLogger(os.Stderr, false)

func NewLogger(out io.Writer, jsonMode bool) *Logger {
	return &Logger{out: out, json: jsonMode, fields: make(map[string]any)}
}

func (l *Logger) With(fields map[string]any) *Logger {
	merged := make(map[string]any, len(l.fields)+len(fields))
	for k, v := range l.fields {
		merged[k] = v
	}
	for k, v := range fields {
		merged[k] = v
	}
	return &Logger{out: l.out, json: l.json, fields: merged}
}

func (l *Logger) log(level LogLevel, msg string, fields map[string]any) {
	if !l.json {
		l.text(level, msg, fields)
		return
	}
	record := make(map[string]any, len(l.fields)+len(fields)+3)
	record["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	record["level"] = string(level)
	record["msg"] = msg
	for k, v := range l.fields {
		record[k] = v
	}
	for k, v := range fields {
		record[k] = v
	}
	buf, err := json.Marshal(record)
	if err != nil {
		_, _ = fmt.Fprintf(l.out, `{"ts":%q,"level":"error","msg":"log marshal failed","err":%q}`+"\n",
			time.Now().UTC().Format(time.RFC3339Nano), err.Error())
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.out.Write(buf)
	_, _ = l.out.Write([]byte("\n"))
}

func (l *Logger) text(level LogLevel, msg string, fields map[string]any) {
	ts := time.Now().Format("2006-01-02T15:04:05.000Z07:00")
	var b []byte
	b = append(b, ts...)
	b = append(b, ' ')
	b = append(b, '[')
	b = append(b, level...)
	b = append(b, ']')
	b = append(b, ' ')
	b = append(b, msg...)
	if len(l.fields) > 0 || len(fields) > 0 {
		b = append(b, ' ')
		b = append(b, '{')
		first := true
		emit := func(k string, v any) {
			if !first {
				b = append(b, ',', ' ')
			}
			first = false
			b = append(b, k...)
			b = append(b, '=')
			b = append(b, fmt.Sprintf("%v", v)...)
		}
		for k, v := range l.fields {
			emit(k, v)
		}
		for k, v := range fields {
			emit(k, v)
		}
		b = append(b, '}')
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.out.Write(b)
}

func (l *Logger) Debug(msg string, fields ...Field) { l.log(LevelDebug, msg, fieldsToMap(fields)) }
func (l *Logger) Info(msg string, fields ...Field)  { l.log(LevelInfo, msg, fieldsToMap(fields)) }
func (l *Logger) Warn(msg string, fields ...Field)  { l.log(LevelWarn, msg, fieldsToMap(fields)) }
func (l *Logger) Error(msg string, fields ...Field) { l.log(LevelError, msg, fieldsToMap(fields)) }

type Field struct {
	Key string
	Val any
}

func F(k string, v any) Field { return Field{Key: k, Val: v} }

func fieldsToMap(fields []Field) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	m := make(map[string]any, len(fields))
	for _, f := range fields {
		m[f.Key] = f.Val
	}
	return m
}
