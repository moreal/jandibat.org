// Package fakedb provides a small scripted database/sql driver for adapter tests.
package fakedb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

type Operation string

const (
	Query    Operation = "query"
	Exec     Operation = "exec"
	Begin    Operation = "begin"
	Commit   Operation = "commit"
	Rollback Operation = "rollback"
)

type Step struct {
	Operation Operation
	Columns   []string
	Rows      [][]driver.Value
	Affected  int64
	Err       error
}

type Call struct {
	Operation Operation
	Query     string
	Args      []driver.NamedValue
}

type Script struct {
	mu    sync.Mutex
	steps []Step
	calls []Call
}

func New(steps ...Step) *Script {
	return &Script{steps: append([]Step(nil), steps...)}
}

func (script *Script) Open() *sql.DB {
	name := fmt.Sprintf("jandibat-fakedb-%d", atomic.AddUint64(&driverSequence, 1))
	sql.Register(name, scriptedDriver{script: script})
	db, err := sql.Open(name, "")
	if err != nil {
		panic(err)
	}
	return db
}

func (script *Script) Calls() []Call {
	script.mu.Lock()
	defer script.mu.Unlock()
	result := make([]Call, len(script.calls))
	copy(result, script.calls)
	return result
}

func (script *Script) Remaining() int {
	script.mu.Lock()
	defer script.mu.Unlock()
	return len(script.steps)
}

func (script *Script) next(call Call) (Step, error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	script.calls = append(script.calls, call)
	if len(script.steps) == 0 {
		return Step{}, fmt.Errorf("fakedb: unexpected %s", call.Operation)
	}
	step := script.steps[0]
	script.steps = script.steps[1:]
	if step.Operation != call.Operation {
		return Step{}, fmt.Errorf("fakedb: got %s, want %s", call.Operation, step.Operation)
	}
	return step, nil
}

var driverSequence uint64

type scriptedDriver struct{ script *Script }

func (driver scriptedDriver) Open(string) (driver.Conn, error) {
	return &connection{script: driver.script}, nil
}

type connection struct{ script *Script }

func (*connection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fakedb: prepared statements are not supported")
}

func (*connection) Close() error { return nil }

func (*connection) CheckNamedValue(*driver.NamedValue) error { return nil }

func (connection *connection) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}

func (connection *connection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	step, err := connection.script.next(Call{Operation: Begin})
	if err != nil {
		return nil, err
	}
	if step.Err != nil {
		return nil, step.Err
	}
	return &transaction{script: connection.script}, nil
}

func (connection *connection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	step, err := connection.script.next(Call{Operation: Query, Query: query, Args: cloneArgs(args)})
	if err != nil {
		return nil, err
	}
	if step.Err != nil {
		return nil, step.Err
	}
	return &rows{columns: append([]string(nil), step.Columns...), values: step.Rows}, nil
}

func (connection *connection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	step, err := connection.script.next(Call{Operation: Exec, Query: query, Args: cloneArgs(args)})
	if err != nil {
		return nil, err
	}
	if step.Err != nil {
		return nil, step.Err
	}
	return driver.RowsAffected(step.Affected), nil
}

type transaction struct{ script *Script }

func (transaction *transaction) Commit() error {
	step, err := transaction.script.next(Call{Operation: Commit})
	if err != nil {
		return err
	}
	return step.Err
}

func (transaction *transaction) Rollback() error {
	step, err := transaction.script.next(Call{Operation: Rollback})
	if err != nil {
		return err
	}
	return step.Err
}

type rows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (rows *rows) Columns() []string { return rows.columns }
func (rows *rows) Close() error      { return nil }

func (rows *rows) Next(dest []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(dest, rows.values[rows.index])
	rows.index++
	return nil
}

func cloneArgs(args []driver.NamedValue) []driver.NamedValue {
	result := make([]driver.NamedValue, len(args))
	copy(result, args)
	return result
}
