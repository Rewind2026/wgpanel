package domain

import (
	"errors"
	"fmt"
	"net/http"
)

// Error 是携带 HTTP 状态码与业务错误码的领域错误。
type Error struct {
	HTTPStatus int
	Code       int
	Message    string
	Err        error
}

// Error 实现 error 接口。
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

// Unwrap 暴露底层错误。
func (e *Error) Unwrap() error { return e.Err }

// 业务错误码分段：4xxxx 客户端错误，5xxxx 服务端错误。
const (
	CodeInvalidArgument = 40000
	CodeUnauthorized    = 40100
	CodeForbidden       = 40300
	CodeNotFound        = 40400
	CodeConflict        = 40900
	CodeRateLimited     = 42900
	CodeInternal        = 50000
)

// ErrInvalid 构造参数非法错误。
func ErrInvalid(format string, args ...any) *Error {
	return &Error{HTTPStatus: http.StatusBadRequest, Code: CodeInvalidArgument, Message: fmt.Sprintf(format, args...)}
}

// ErrUnauthorized 构造未认证错误。
func ErrUnauthorized(format string, args ...any) *Error {
	return &Error{HTTPStatus: http.StatusUnauthorized, Code: CodeUnauthorized, Message: fmt.Sprintf(format, args...)}
}

// ErrForbidden 构造权限不足错误。
func ErrForbidden(format string, args ...any) *Error {
	return &Error{HTTPStatus: http.StatusForbidden, Code: CodeForbidden, Message: fmt.Sprintf(format, args...)}
}

// ErrNotFound 构造资源不存在错误。
func ErrNotFound(format string, args ...any) *Error {
	return &Error{HTTPStatus: http.StatusNotFound, Code: CodeNotFound, Message: fmt.Sprintf(format, args...)}
}

// ErrConflict 构造资源冲突错误。
func ErrConflict(format string, args ...any) *Error {
	return &Error{HTTPStatus: http.StatusConflict, Code: CodeConflict, Message: fmt.Sprintf(format, args...)}
}

// ErrRateLimited 构造限流错误。
func ErrRateLimited(format string, args ...any) *Error {
	return &Error{HTTPStatus: http.StatusTooManyRequests, Code: CodeRateLimited, Message: fmt.Sprintf(format, args...)}
}

// ErrInternal 构造内部错误。
func ErrInternal(err error) *Error {
	return &Error{HTTPStatus: http.StatusInternalServerError, Code: CodeInternal, Message: "internal server error", Err: err}
}

// AsError 将任意 error 归一化为 *Error。
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var de *Error
	if errors.As(err, &de) {
		return de
	}
	return ErrInternal(err)
}
