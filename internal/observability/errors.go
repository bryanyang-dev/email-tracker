package observability

import "errors"

type FailureReason string

type failureReasoner interface {
	FailureReason() FailureReason
}

func ReasonFromError(err error, fallback FailureReason) FailureReason {
	var reasonedError failureReasoner
	if errors.As(err, &reasonedError) {
		if reason := reasonedError.FailureReason(); reason != "" {
			return reason
		}
	}
	return fallback
}
