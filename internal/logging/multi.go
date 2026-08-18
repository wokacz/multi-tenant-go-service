package logging

import (
	"context"
	"errors"
	"log/slog"
)

// Fanout sends every record to more than one handler.
//
// It exists because a log line has two readers with different needs: a person
// watching a terminal, and whatever collects logs for the installation. Choosing
// one would mean either losing colour or losing the collector, and writing the
// record twice at each call site would mean forgetting one.
//
// Handlers are called in order and every one is called even if an earlier one
// fails, because a broken exporter must not stop the line reaching the terminal.
// The errors are joined so the failure is still reportable.
func Fanout(handlers ...slog.Handler) slog.Handler {
	kept := make([]slog.Handler, 0, len(handlers))

	for _, handler := range handlers {
		if handler != nil {
			kept = append(kept, handler)
		}
	}

	if len(kept) == 1 {
		return kept[0]
	}

	return &fanout{handlers: kept}
}

type fanout struct {
	handlers []slog.Handler
}

var _ slog.Handler = (*fanout)(nil)

// Enabled is true when any handler wants the record. A console filtered to info
// must not silence the level a collector was configured to keep.
func (f *fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range f.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}

	return false
}

func (f *fanout) Handle(ctx context.Context, record slog.Record) error {
	var errs []error

	for _, handler := range f.handlers {
		// Asked again per handler: this is the only place that knows one of them
		// might not want a record the others do.
		if !handler.Enabled(ctx, record.Level) {
			continue
		}

		// Each handler gets its own clone. A handler is allowed to consume the
		// record's attributes, and slog.Record is a value with shared backing
		// storage — handing the same one to two handlers is how attributes go
		// missing from the second.
		if err := handler.Handle(ctx, record.Clone()); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func (f *fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, 0, len(f.handlers))
	for _, handler := range f.handlers {
		next = append(next, handler.WithAttrs(attrs))
	}

	return &fanout{handlers: next}
}

func (f *fanout) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, 0, len(f.handlers))
	for _, handler := range f.handlers {
		next = append(next, handler.WithGroup(name))
	}

	return &fanout{handlers: next}
}
