package store

import (
	"database/sql"
	"strconv"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// dialector is the minimal GORM dialect for the embedded SQLite driver
// (modernc.org/sqlite, pure Go, no CGO). It exists so the project does not
// depend on a CGO driver or a second SQLite build. Schema is created by the
// versioned migrations, never by AutoMigrate, so the migrator is the base one.
type dialector struct{ conn *sql.DB }

func (dialector) Name() string { return "sqlite" }

func (d dialector) Initialize(db *gorm.DB) error {
	db.ConnPool = d.conn
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{
		CreateClauses:        []string{"INSERT", "VALUES", "ON CONFLICT", "RETURNING"},
		UpdateClauses:        []string{"UPDATE", "SET", "FROM", "WHERE", "RETURNING"},
		DeleteClauses:        []string{"DELETE", "FROM", "WHERE", "RETURNING"},
		LastInsertIDReversed: true,
	})
	for k, v := range d.clauseBuilders() {
		db.ClauseBuilders[k] = v
	}
	return nil
}

func (dialector) clauseBuilders() map[string]clause.ClauseBuilder {
	return map[string]clause.ClauseBuilder{
		"LIMIT": func(c clause.Clause, builder clause.Builder) {
			if limit, ok := c.Expression.(clause.Limit); ok {
				n := -1
				if limit.Limit != nil && *limit.Limit >= 0 {
					n = *limit.Limit
				}
				if n >= 0 || limit.Offset > 0 {
					builder.WriteString("LIMIT " + strconv.Itoa(n))
				}
				if limit.Offset > 0 {
					builder.WriteString(" OFFSET " + strconv.Itoa(limit.Offset))
				}
			}
		},
		// SQLite has no row locks. Every write transaction starts with
		// BEGIN IMMEDIATE, which serializes writers for the whole database, so
		// FOR UPDATE / SKIP LOCKED is dropped rather than emulated.
		"FOR": func(c clause.Clause, builder clause.Builder) {
			if _, ok := c.Expression.(clause.Locking); ok {
				return
			}
			c.Build(builder)
		},
	}
}

func (dialector) DefaultValueOf(f *schema.Field) clause.Expression {
	if f.AutoIncrement {
		return clause.Expr{SQL: "NULL"}
	}
	return clause.Expr{SQL: "DEFAULT"}
}

func (d dialector) Migrator(db *gorm.DB) gorm.Migrator {
	return migrator.Migrator{Config: migrator.Config{DB: db, Dialector: d}}
}

func (dialector) BindVarTo(w clause.Writer, _ *gorm.Statement, _ interface{}) { w.WriteByte('?') }

func (dialector) QuoteTo(w clause.Writer, s string) {
	w.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			w.WriteString(`""`)
			continue
		}
		w.WriteByte(s[i])
	}
	w.WriteByte('"')
}

func (dialector) Explain(sql string, _ ...interface{}) string { return sql }

func (dialector) DataTypeOf(f *schema.Field) string {
	switch f.DataType {
	case schema.Bool:
		return "boolean"
	case schema.Int, schema.Uint:
		return "integer"
	case schema.Float:
		return "real"
	case schema.String:
		return "text"
	case schema.Time:
		return "timestamp"
	case schema.Bytes:
		return "blob"
	}
	return string(f.DataType)
}

// SavePoint and RollbackTo let nested gorm transactions run inside the
// immediate transaction that already holds the writer lock.
func (dialector) SavePoint(tx *gorm.DB, name string) error {
	return tx.Exec("SAVEPOINT " + name).Error
}
func (dialector) RollbackTo(tx *gorm.DB, name string) error {
	return tx.Exec("ROLLBACK TO SAVEPOINT " + name).Error
}

// jsonText converts raw JSON to a string so SQLite stores it as TEXT. A
// []byte argument would be bound as a BLOB, which the JSON functions and
// json_valid constraints treat as JSONB. Nil stays SQL NULL.
func jsonText(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// JSONText is the exported form for other packages writing JSON columns
// through map-based GORM updates, which bypass field serializers.
func JSONText(raw []byte) any { return jsonText(raw) }
