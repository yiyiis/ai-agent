// Package inputtestdb provides an offline GORM fixture for interaction tests.
// It exercises DAO transactions and uniqueness without contacting real MySQL.
package inputtestdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"backend/dal/model"
	"backend/dal/query"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type Store struct {
	mu       sync.Mutex
	Messages []model.Message
	Locks    int
}

func New(t *testing.T, messages ...model.Message) (context.Context, *Store) {
	t.Helper()
	s := &Store{Messages: append([]model.Message(nil), messages...)}
	conn := sql.OpenDB(connector{s})
	t.Cleanup(func() { _ = conn.Close() })
	g, err := gorm.Open(mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true}),
		&gorm.Config{DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	lock := func(tx *gorm.DB) func() {
		if _, transactional := tx.Statement.ConnPool.(*sql.Tx); transactional {
			return func() {}
		}
		s.mu.Lock()
		return s.mu.Unlock
	}
	err = g.Callback().Query().Replace("gorm:query", func(tx *gorm.DB) {
		defer lock(tx)()
		switch tx.Statement.Table {
		case "sessions":
			if _, ok := tx.Statement.Clauses["FOR"]; ok {
				s.Locks++
			}
			if dest, ok := tx.Statement.Dest.(*model.Session); ok {
				*dest = model.Session{SessionID: "session", Title: "existing", Model: "test-model", UserID: 7, CompanyID: 9}
				tx.RowsAffected = 1
			}
		case "messages":
			switch dest := tx.Statement.Dest.(type) {
			case *[]*model.Message:
				for _, m := range s.Messages {
					copy := m
					*dest = append(*dest, &copy)
				}
				tx.RowsAffected = int64(len(s.Messages))
			default:
				tx.AddError(fmt.Errorf("unsupported message query %T", tx.Statement.Dest))
			}
		default:
			// Skill/MCP/usage collections are empty in this isolated fixture.
			dest := reflect.ValueOf(tx.Statement.Dest)
			if dest.Kind() == reflect.Pointer && dest.Elem().Kind() == reflect.Slice {
				dest.Elem().Set(reflect.MakeSlice(dest.Elem().Type(), 0, 0))
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	err = g.Callback().Create().Replace("gorm:create", func(tx *gorm.DB) {
		defer lock(tx)()
		add := func(m *model.Message) {
			for _, old := range s.Messages {
				if old.MessageID == m.MessageID {
					tx.AddError(fmt.Errorf("duplicate message_id"))
					return
				}
			}
			m.ID = int64(len(s.Messages) + 1)
			if m.CreatedAt.IsZero() {
				m.CreatedAt = time.Now()
			}
			s.Messages = append(s.Messages, *m)
			tx.RowsAffected++
		}
		switch dest := tx.Statement.Dest.(type) {
		case *model.Message:
			add(dest)
		case []*model.Message:
			for _, m := range dest {
				add(m)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return context.WithValue(context.Background(), "gormDb", query.Use(g)), s
}

func (s *Store) Snapshot() []model.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.Message(nil), s.Messages...)
}

type connector struct{ s *Store }

func (c connector) Connect(context.Context) (driver.Conn, error) { return &connection{s: c.s}, nil }
func (c connector) Driver() driver.Driver                        { return dummyDriver{} }

type dummyDriver struct{}

func (dummyDriver) Open(string) (driver.Conn, error) { return nil, fmt.Errorf("use connector") }

type connection struct{ s *Store }

func (c *connection) Prepare(string) (driver.Stmt, error) { return nil, fmt.Errorf("unexpected SQL") }
func (c *connection) Close() error                        { return nil }
func (c *connection) Begin() (driver.Tx, error) {
	c.s.mu.Lock()
	return &transaction{s: c.s, before: append([]model.Message(nil), c.s.Messages...)}, nil
}

type transaction struct {
	s      *Store
	before []model.Message
}

func (t *transaction) Commit() error   { t.s.mu.Unlock(); return nil }
func (t *transaction) Rollback() error { t.s.Messages = t.before; t.s.mu.Unlock(); return nil }
