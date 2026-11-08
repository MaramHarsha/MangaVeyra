package jobs

import (
	"context"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// publicErrorCodes are the stable failure codes a job may show outside the
// server. Workers and providers can report any code string, so every other
// value is replaced by ErrCodeInternal before it leaves the server.
var publicErrorCodes = map[string]bool{
	"provider_auth": true, "no_provider": true, "rate_limited": true, ErrCodeTimeout: true, ErrCodeOOM: true,
	"scope_locked": true, "stale": true, "conflict": true, "spend_limit": true, "provider_refused": true,
	ErrCodeSafety: true, ErrCodeBadInput: true, ErrCodeCanceled: true, "local_only": true, "upstream": true,
	"bad_output": true, "approval_required": true, ErrCodeInternal: true,
}

// describedErrorCodes have their own recovery copy (activity.error.<code>).
// The remaining public codes use the generic "review it in Studio" copy.
var describedErrorCodes = map[string]bool{
	"provider_auth": true, "no_provider": true, "rate_limited": true, ErrCodeTimeout: true, ErrCodeOOM: true,
	"scope_locked": true, "stale": true, "conflict": true, "spend_limit": true, "provider_refused": true,
	ErrCodeSafety: true, ErrCodeBadInput: true, ErrCodeCanceled: true, "local_only": true,
}

// PublicErrorCode returns code when it is a stable public failure code and
// ErrCodeInternal otherwise.
func PublicErrorCode(code string) string {
	if publicErrorCodes[code] {
		return code
	}
	return ErrCodeInternal
}

// PublicErrorKey is the message key of the localized, safe recovery copy for
// a failure code. Raw worker/provider messages are never shown to members:
// they can contain prompts, private project text or credentials.
func PublicErrorKey(code string) string {
	if describedErrorCodes[code] {
		return "activity.error." + code
	}
	return "activity.error.job"
}

// publicEventKinds are the server-defined event kinds the job API may name;
// other kinds read "update".
var publicEventKinds = map[string]bool{
	"created": true, "claimed": true, "progress": true, "artifact": true, "done": true, "failed": true,
	"canceled": true, "cancel_requested": true, "retried": true, "retry": true, "fallback": true, "log": true,
}

// PublicView is PublicViewFor with the fallback-language recovery copy.
func (q *Queue) PublicView(j *Job) *Job { return q.PublicViewFor(context.Background(), j) }

// PublicViewFor returns j as the job API may show it to anyone, including its
// owner, admins and bearer tokens: the input is removed when the job type's
// input is private, progress text is dropped and a failure carries only its
// public code with recovery copy in the locale of ctx. Raw worker/provider
// text stays in the database for handlers; it never leaves the server.
func (q *Queue) PublicViewFor(ctx context.Context, j *Job) *Job {
	if j == nil {
		return nil
	}
	q.mu.RLock()
	private := q.privateInput[j.Type]
	q.mu.RUnlock()
	c := *j
	if private {
		c.Input = nil
	}
	c.Message = ""
	if j.Error != nil {
		code := PublicErrorCode(j.Error.Code)
		c.Error = &JobError{Code: code, Message: i18n.T(ctx, PublicErrorKey(code)), Retryable: j.Error.Retryable}
	}
	return &c
}

// PublicEvents returns a job log as the job API may show it: event kinds and
// timestamps only. Event messages and data can carry provider responses,
// fallback errors or storage keys.
func PublicEvents(events []Event) []Event {
	out := make([]Event, 0, len(events))
	for _, ev := range events {
		kind := ev.Kind
		if !publicEventKinds[kind] {
			kind = "update"
		}
		out = append(out, Event{ID: ev.ID, JobID: ev.JobID, Kind: kind, CreatedAt: ev.CreatedAt})
	}
	return out
}
