package main

// Failures, recoverable or not. A dropped connection, a throttled request or a
// server's own error passes, and the job is attempted again; a file that is
// gone or forbidden, one that is not what it claims to be, or one a routine
// cannot process fails the same way every time.

import (
	"regexp"
	"time"
)

var transient_failure = regexp.MustCompile(`(?i)` +
	`connection reset|connection refused|broken pipe|unexpected EOF|` +
	`timeout|deadline exceeded|context canceled|TLS handshake|no such host|temporary failure|` +
	`Recv failure|Send failure|Could not resolve|upload failed|` +
	`HTTP response code: (429|5\d\d)|\b(429|5\d\d) [A-Z][a-z]|` +
	// GDAL opening a URL whose HEAD has just answered.
	`dataset 'https?://[^']*' open(Ex)? error`)

// transient says the failure may not recur.
func transient(msg string) bool {
	return transient_failure.MatchString(msg)
}

const failure_attempts = 3

// failure_pause is the wait before attempt+1: 10s, then 40s.
func failure_pause(attempt int) time.Duration {
	return time.Duration(attempt*attempt) * 10 * time.Second
}
