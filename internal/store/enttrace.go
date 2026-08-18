package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	"entgo.io/ent/dialect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/wokacz/multi-tenant-go-service/internal/telemetry"
)

// tracedDriver is the ent half of D5: a dialect.Driver that turns every statement
// into a span, without depending on GORM's callback API.
//
// It wraps the driver the ent client already has, so GORM's callbacks keep covering
// the repositories that have not moved yet and this covers the ones that have. The
// two must not stack on the same statement — this wrapper is only on the ent client,
// never on the *sql.DB GORM holds.
//
// Bound values never enter the span. The query string already carries $1, $2, …;
// the args stay in the call and out of the attributes. That is the same rule as
// the GORM callbacks, and TestASpanNeverCarriesQueryValues is the acceptance test
// for both.
type tracedDriver struct {
	dialect.Driver
	tel atomic.Pointer[telemetry.Telemetry]
}

func newTracedDriver(inner dialect.Driver) *tracedDriver {
	return &tracedDriver{Driver: inner}
}

var _ dialect.Driver = (*tracedDriver)(nil)

func (d *tracedDriver) bind(tel *telemetry.Telemetry) {
	d.tel.Store(tel)
}

func (d *tracedDriver) Exec(ctx context.Context, query string, args, v any) error {
	ctx, end := d.trace(ctx, query)
	return end(d.Driver.Exec(ctx, query, args, v))
}

func (d *tracedDriver) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	drv, ok := d.Driver.(interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	})
	if !ok {
		return nil, fmt.Errorf("store: driver does not support ExecContext")
	}

	ctx, end := d.trace(ctx, query)
	res, err := drv.ExecContext(ctx, query, args...)
	return res, end(err)
}

func (d *tracedDriver) Query(ctx context.Context, query string, args, v any) error {
	ctx, end := d.trace(ctx, query)
	return end(d.Driver.Query(ctx, query, args, v))
}

func (d *tracedDriver) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	drv, ok := d.Driver.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	if !ok {
		return nil, fmt.Errorf("store: driver does not support QueryContext")
	}

	ctx, end := d.trace(ctx, query)
	rows, err := drv.QueryContext(ctx, query, args...)
	return rows, end(err)
}

func (d *tracedDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}

	return &tracedTx{Tx: tx, driver: d}, nil
}

func (d *tracedDriver) BeginTx(ctx context.Context, opts *sql.TxOptions) (dialect.Tx, error) {
	drv, ok := d.Driver.(interface {
		BeginTx(context.Context, *sql.TxOptions) (dialect.Tx, error)
	})
	if !ok {
		return d.Tx(ctx)
	}

	tx, err := drv.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}

	return &tracedTx{Tx: tx, driver: d}, nil
}

type tracedTx struct {
	dialect.Tx
	driver *tracedDriver
}

func (t *tracedTx) Exec(ctx context.Context, query string, args, v any) error {
	ctx, end := t.driver.trace(ctx, query)
	return end(t.Tx.Exec(ctx, query, args, v))
}

func (t *tracedTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	drv, ok := t.Tx.(interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	})
	if !ok {
		return nil, fmt.Errorf("store: tx does not support ExecContext")
	}

	ctx, end := t.driver.trace(ctx, query)
	res, err := drv.ExecContext(ctx, query, args...)
	return res, end(err)
}

func (t *tracedTx) Query(ctx context.Context, query string, args, v any) error {
	ctx, end := t.driver.trace(ctx, query)
	return end(t.Tx.Query(ctx, query, args, v))
}

func (t *tracedTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	drv, ok := t.Tx.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	if !ok {
		return nil, fmt.Errorf("store: tx does not support QueryContext")
	}

	ctx, end := t.driver.trace(ctx, query)
	rows, err := drv.QueryContext(ctx, query, args...)
	return rows, end(err)
}

func (d *tracedDriver) trace(ctx context.Context, query string) (context.Context, func(error) error) {
	tel := d.tel.Load()
	if tel == nil || !tel.Enabled || ctx == nil {
		return ctx, func(err error) error { return err }
	}

	operation := operationOf(query)
	table := tableOf(query, "")
	name := operation
	if table != "" {
		name = operation + " " + table
	}

	started := time.Now()
	ctx, span := tel.Tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient))

	return ctx, func(err error) error {
		defer span.End()

		span.SetAttributes(
			attribute.String("db.system.name", "postgresql"),
			attribute.String("db.collection.name", table),
			attribute.String("db.operation.name", operation),
			// Placeholders, never values. args are deliberately not here.
			attribute.String("db.query.text", query),
		)

		failed := err != nil
		if failed {
			span.RecordError(err)
			span.SetStatus(codes.Error, "query failed")
		}

		attrs := metric.WithAttributes(
			attribute.String(telemetry.AttrOperation, operation),
			attribute.String(telemetry.AttrTable, table),
			attribute.Bool(telemetry.AttrError, failed),
		)

		if tel.Metrics != nil && tel.Metrics.DBQueries != nil {
			tel.Metrics.DBQueries.Add(ctx, 1, attrs)
		}

		if tel.Metrics != nil && tel.Metrics.DBDuration != nil {
			tel.Metrics.DBDuration.Record(ctx, time.Since(started).Seconds(), attrs)
		}

		return err
	}
}
