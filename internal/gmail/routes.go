package gmail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// credClass names the credential slot an operation is allowed to use.
type credClass uint8

const (
	classRead credClass = iota
	classWrite
	classSend
)

// operation names one Gmail API call mailbox may make. The route table below
// is the ONE allowlist owning both the endpoint template and the credential
// class; helpers select an operation and can no longer pair an arbitrary path
// with an arbitrary credential. There is deliberately NO drafts.send
// operation: gmail.modify technically permits that route, and mailbox refuses
// it by construction (spec §4).
type operation uint8

const (
	opThreadsList operation = iota
	opThreadsGet
	opThreadsModify
	opThreadsTrash
	opMessagesGet
	opAttachmentsGet
	opLabelsList
	opProfileGet
	opMessagesSend
	opDraftsList
	opDraftsGet
	opDraftsCreate
	opDraftsDelete
	operationCount
)

type route struct {
	method   string
	template string // fmt template; every verb is one url.PathEscape'd argument
	class    credClass
	scope    string // Gmail scope named in insufficient-scope errors
}

var routes = [operationCount]route{
	opThreadsList:    {http.MethodGet, "/gmail/v1/users/me/threads", classRead, "gmail.readonly"},
	opThreadsGet:     {http.MethodGet, "/gmail/v1/users/me/threads/%s", classRead, "gmail.readonly"},
	opThreadsModify:  {http.MethodPost, "/gmail/v1/users/me/threads/%s/modify", classWrite, "gmail.modify"},
	opThreadsTrash:   {http.MethodPost, "/gmail/v1/users/me/threads/%s/trash", classWrite, "gmail.modify"},
	opMessagesGet:    {http.MethodGet, "/gmail/v1/users/me/messages/%s", classRead, "gmail.readonly"},
	opAttachmentsGet: {http.MethodGet, "/gmail/v1/users/me/messages/%s/attachments/%s", classRead, "gmail.readonly"},
	opLabelsList:     {http.MethodGet, "/gmail/v1/users/me/labels", classRead, "gmail.readonly"},
	opProfileGet:     {http.MethodGet, "/gmail/v1/users/me/profile", classRead, "gmail.readonly"},
	opMessagesSend:   {http.MethodPost, "/gmail/v1/users/me/messages/send", classSend, "gmail.send"},
	opDraftsList:     {http.MethodGet, "/gmail/v1/users/me/drafts", classRead, "gmail.readonly"},
	opDraftsGet:      {http.MethodGet, "/gmail/v1/users/me/drafts/%s", classRead, "gmail.readonly"},
	opDraftsCreate:   {http.MethodPost, "/gmail/v1/users/me/drafts", classWrite, "gmail.modify"},
	opDraftsDelete:   {http.MethodDelete, "/gmail/v1/users/me/drafts/%s", classWrite, "gmail.modify"},
}

func (c *Client) credentials(class credClass) (Credentials, error) {
	switch class {
	case classRead:
		return c.read, nil
	case classWrite:
		if c.write == nil {
			return nil, errors.New("gmail: client has no write credentials")
		}
		return c.write, nil
	case classSend:
		if c.send == nil {
			return nil, errors.New("gmail: client has no send credentials")
		}
		return c.send, nil
	}
	return nil, fmt.Errorf("gmail: unknown credential class %d", class)
}

func routePath(op operation, args []string) string {
	escaped := make([]any, len(args))
	for index, arg := range args {
		escaped[index] = url.PathEscape(arg)
	}
	return fmt.Sprintf(routes[op].template, escaped...)
}

// call resolves op through the allowlist and dispatches the request.
func (c *Client) call(ctx context.Context, op operation, args []string, query url.Values, body, out any) error {
	creds, err := c.credentials(routes[op].class)
	if err != nil {
		return err
	}
	return c.scopeMapped(c.do(ctx, creds, routes[op].method, routePath(op, args), query, body, out), routes[op].scope)
}
