package model

import "errors"

var ErrNotFound = errors.New("not found")

var ErrConflict = errors.New("conflict")

var ErrInvalid = errors.New("invalid input")

// conflictError is an ErrConflict that says which conflict it is: the 409 body
// carries Error() as is, so the panel and MCP can tell the admin what to do
// instead of one catch-all line.
type conflictError struct{ msg string }

func (e conflictError) Error() string { return e.msg }

func (e conflictError) Is(target error) bool { return target == ErrConflict }

// Conflict returns an error that matches ErrConflict and reads as msg.
func Conflict(msg string) error { return conflictError{msg} }

// invalidError is an ErrInvalid that reads as its own message (a 400 body).
type invalidError struct{ msg string }

func (e invalidError) Error() string { return e.msg }

func (e invalidError) Is(target error) bool { return target == ErrInvalid }

// Invalid returns an error that matches ErrInvalid and reads as msg.
func Invalid(msg string) error { return invalidError{msg} }

// notFoundError is an ErrNotFound that names what was missing.
type notFoundError struct{ msg string }

func (e notFoundError) Error() string { return e.msg }

func (e notFoundError) Is(target error) bool { return target == ErrNotFound }

// NotFound returns an error that matches ErrNotFound and reads as msg.
func NotFound(msg string) error { return notFoundError{msg} }
