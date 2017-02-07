// Package sqlite3 implements the Driver interface.
package sqlite3

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/db-journey/migrate/direction"
	"github.com/db-journey/migrate/driver"
	"github.com/db-journey/migrate/file"
	"github.com/mattn/go-sqlite3"
)

type Driver struct {
	db *sql.DB
}

const tableName = "schema_migration"

func (driver *Driver) Initialize(url string) error {
	filename := strings.SplitN(url, "sqlite3://", 2)
	if len(filename) != 2 {
		return errors.New("invalid sqlite3:// scheme")
	}

	db, err := sql.Open("sqlite3", filename[1])
	if err != nil {
		return err
	}
	if err := db.Ping(); err != nil {
		return err
	}
	driver.db = db

	if err := driver.ensureVersionTableExists(); err != nil {
		return err
	}
	return nil
}

func (driver *Driver) Close() error {
	if err := driver.db.Close(); err != nil {
		return err
	}
	return nil
}

func (driver *Driver) ensureVersionTableExists() error {
	if _, err := driver.db.Exec("CREATE TABLE IF NOT EXISTS " + tableName + " (version INTEGER PRIMARY KEY AUTOINCREMENT);"); err != nil {
		return err
	}
	return nil
}

func (driver *Driver) FilenameExtension() string {
	return "sql"
}

func (driver *Driver) Migrate(f file.File, pipe chan interface{}) {
	defer close(pipe)
	pipe <- f

	tx, err := driver.db.Begin()
	if err != nil {
		pipe <- err
		return
	}

	if f.Direction == direction.Up {
		if _, err := tx.Exec("INSERT INTO "+tableName+" (version) VALUES (?)", f.Version); err != nil {
			pipe <- err
			if err := tx.Rollback(); err != nil {
				pipe <- err
			}
			return
		}
	} else if f.Direction == direction.Down {
		if _, err := tx.Exec("DELETE FROM "+tableName+" WHERE version=?", f.Version); err != nil {
			pipe <- err
			if err := tx.Rollback(); err != nil {
				pipe <- err
			}
			return
		}
	}

	if err := f.ReadContent(); err != nil {
		pipe <- err
		return
	}

	queries := splitStatements(string(f.Content))
	for _, query := range queries {
		if _, err := tx.Exec(query); err != nil {
			sqliteErr, isErr := err.(sqlite3.Error)
			if isErr {
				// The sqlite3 library only provides error codes, not position information. Output what we do know.
				pipe <- fmt.Errorf("SQLite Error (%s); Extended (%s)\nError: %s",
					sqliteErr.Code.Error(), sqliteErr.ExtendedCode.Error(), sqliteErr.Error())
			} else {
				pipe <- fmt.Errorf("An error occurred when running query [%q]: %v", query, err)
			}
			if err := tx.Rollback(); err != nil {
				pipe <- err
			}
			return
		}
	}

	if err := tx.Commit(); err != nil {
		pipe <- err
		return
	}
}

// Version returns the current migration version.
func (driver *Driver) Version() (file.Version, error) {
	var version file.Version
	err := driver.db.QueryRow("SELECT version FROM " + tableName + " ORDER BY version DESC LIMIT 1").Scan(&version)
	switch {
	case err == sql.ErrNoRows:
		return 0, nil
	case err != nil:
		return 0, err
	default:
		return version, nil
	}
}

// Versions returns the list of applied migrations.
func (driver *Driver) Versions() (file.Versions, error) {
	versions := file.Versions{}

	rows, err := driver.db.Query("SELECT version FROM " + tableName + " ORDER BY version DESC")
	if err != nil {
		return versions, err
	}
	defer rows.Close()
	for rows.Next() {
		var version file.Version
		err := rows.Scan(&version)
		if err != nil {
			return versions, err
		}
		versions = append(versions, version)
	}
	err = rows.Err()
	return versions, err
}

func init() {
	driver.RegisterDriver("sqlite3", &Driver{})
}

// This naive implementation doesn't account for quoted ";" inside statements.
// It should work for most migrations but can be improved in the future.
func splitStatements(in string) []string {
	result := make([]string, 0)

	qs := strings.Split(in, ";")
	for _, q := range qs {
		if q = strings.TrimSpace(q); q != "" {
			result = append(result, q+";")
		}
	}
	return result
}
