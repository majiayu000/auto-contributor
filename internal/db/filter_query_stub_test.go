package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync/atomic"
	"testing"
)

const filterQueryPostgresStubDriverName = "filter_query_postgres_stub"

var filterQueryPostgresStubValue atomic.Pointer[filterQueryPostgresStub]

func init() {
	sql.Register(filterQueryPostgresStubDriverName, filterQueryPostgresStubDriver{})
}

func registerFilterQueryPostgresStub(t *testing.T, stub *filterQueryPostgresStub) {
	t.Helper()
	filterQueryPostgresStubValue.Store(stub)
	t.Cleanup(func() {
		filterQueryPostgresStubValue.Store(nil)
	})
}

type filterQueryPostgresStub struct {
	columns    []string
	rows       [][]driver.Value
	queryCount int
	lastQuery  string
	lastArgs   []driver.Value
}

type filterQueryPostgresStubDriver struct{}

func (filterQueryPostgresStubDriver) Open(string) (driver.Conn, error) {
	stub := filterQueryPostgresStubValue.Load()
	if stub == nil {
		return nil, fmt.Errorf("filter query postgres stub not registered")
	}
	return &filterQueryPostgresStubConn{stub: stub}, nil
}

type filterQueryPostgresStubConn struct {
	stub *filterQueryPostgresStub
}

func (c *filterQueryPostgresStubConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepare not implemented")
}

func (c *filterQueryPostgresStubConn) Close() error {
	return nil
}

func (c *filterQueryPostgresStubConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions not implemented")
}

func (c *filterQueryPostgresStubConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.stub.queryCount++
	c.stub.lastQuery = normalizeWhitespace(query)
	c.stub.lastArgs = namedValuesToValues(args)
	return &createPullRequestPostgresStubRows{
		columns: c.stub.columns,
		rows:    c.stub.rows,
	}, nil
}

func (c *filterQueryPostgresStubConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, fmt.Errorf("unexpected ExecContext call")
}

var _ driver.Conn = (*filterQueryPostgresStubConn)(nil)
var _ driver.QueryerContext = (*filterQueryPostgresStubConn)(nil)
var _ driver.ExecerContext = (*filterQueryPostgresStubConn)(nil)
var _ driver.Conn = (*ruleEmbeddingsPostgresStubConn)(nil)
var _ driver.QueryerContext = (*ruleEmbeddingsPostgresStubConn)(nil)
var _ driver.ExecerContext = (*ruleEmbeddingsPostgresStubConn)(nil)
