package apperror

import (
	"errors"
	"fmt"
	"net/http"
)

type Kind string

const (
	KindBadRequest   Kind = "bad_request"
	KindUnauthorized Kind = "unauthorized"
	KindForbidden    Kind = "forbidden"
	KindNotFound     Kind = "not_found"
	KindGone         Kind = "gone"
	KindConflict     Kind = "conflict"
	KindValidation   Kind = "validation"
	KindInternal     Kind = "internal"
)

type Error struct {
	Kind    Kind
	Message string
	Fields  map[string]string // per-field validation messages
	Err     error             // wrapped underlying error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

func New(kind Kind, msg string) *Error             { return &Error{Kind: kind, Message: msg} }
func Wrap(kind Kind, msg string, err error) *Error { return &Error{Kind: kind, Message: msg, Err: err} }

func BadRequest(msg string) *Error   { return New(KindBadRequest, msg) }
func Unauthorized(msg string) *Error { return New(KindUnauthorized, msg) }
func Forbidden(msg string) *Error    { return New(KindForbidden, msg) }
func NotFound(msg string) *Error     { return New(KindNotFound, msg) }
func Gone(msg string) *Error         { return New(KindGone, msg) }
func Conflict(msg string) *Error     { return New(KindConflict, msg) }
func Internal(msg string, err error) *Error {
	return &Error{Kind: KindInternal, Message: msg, Err: err}
}

func Validation(fields map[string]string) *Error {
	return &Error{Kind: KindValidation, Message: "validation failed", Fields: fields}
}

func (e *Error) HTTPStatus() int {
	switch e.Kind {
	case KindBadRequest, KindValidation:
		return http.StatusBadRequest
	case KindUnauthorized:
		return http.StatusUnauthorized
	case KindForbidden:
		return http.StatusForbidden
	case KindNotFound:
		return http.StatusNotFound
	case KindGone:
		return http.StatusGone
	case KindConflict:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
