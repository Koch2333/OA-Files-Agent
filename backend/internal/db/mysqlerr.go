package db

import (
	"errors"

	"github.com/go-sql-driver/mysql"
)

func mysqlAlreadyDone(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && mysqlAlready[me.Number]
}
