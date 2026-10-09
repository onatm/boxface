package lifecycle

import "errors"

var (
	ErrInvalid             = errors.New("invalid request")
	ErrNotFound            = errors.New("not found")
	ErrNameTaken           = errors.New("environment name already in use")
	ErrEnvironmentDeleted  = errors.New("environment is deleted")
	ErrImageNotReady       = errors.New("image is not ready")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different parameters")
	ErrCapacity            = errors.New("capacity exceeded")
	ErrWorkerUnavailable   = errors.New("worker unavailable")
)
