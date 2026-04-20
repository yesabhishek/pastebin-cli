package errs

import (
	"errors"
	"fmt"
)

type Code int

const (
	CodeUnknown Code = 1 + iota
	CodeUsage
	CodeAuth
	CodeNetwork
	CodeRateLimit
	CodeConflict
	CodeLocalCorruption
	CodeRemoteCorruption
	CodeNotFound
)

type Error struct {
	Code Code
	Err  error
	Msg  string
}

func (e *Error) Error() string {
	switch {
	case e.Msg != "" && e.Err != nil:
		return fmt.Sprintf("%s: %v", e.Msg, e.Err)
	case e.Msg != "":
		return e.Msg
	case e.Err != nil:
		return e.Err.Error()
	default:
		return "unknown error"
	}
}

func (e *Error) Unwrap() error {
	return e.Err
}

func Wrap(code Code, msg string, err error) error {
	if err == nil && msg == "" {
		return nil
	}
	return &Error{Code: code, Msg: msg, Err: err}
}

func WithCode(code Code, err error) error {
	if err == nil {
		return nil
	}
	var coded *Error
	if errors.As(err, &coded) {
		if coded.Code == code {
			return err
		}
	}
	return &Error{Code: code, Err: err}
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded *Error
	if errors.As(err, &coded) {
		return int(coded.Code)
	}
	return int(CodeUnknown)
}

func IsCode(err error, code Code) bool {
	var coded *Error
	return errors.As(err, &coded) && coded.Code == code
}
