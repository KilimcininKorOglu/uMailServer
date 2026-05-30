package jmap

import (
	"encoding/json"
	"fmt"
)

// JMAP represents method calls and responses as an "Invocation" tuple on the
// wire (RFC 8620 §3.2): a 3-element JSON array of [ name, arguments,
// methodCallId ]. Internally we keep the readable struct shape (Name/Args/ID);
// these (un)marshalers bridge the struct and the RFC tuple so the dispatch and
// handler code stays unchanged while the wire format is standards-compliant.

// MarshalJSON encodes a MethodCall as an Invocation tuple.
func (m MethodCall) MarshalJSON() ([]byte, error) {
	args := m.Args
	if args == nil {
		args = map[string]interface{}{}
	}
	return json.Marshal([]interface{}{m.Name, args, m.ID})
}

// UnmarshalJSON decodes an Invocation tuple into a MethodCall.
func (m *MethodCall) UnmarshalJSON(data []byte) error {
	name, args, id, err := decodeInvocation(data)
	if err != nil {
		return err
	}
	m.Name, m.Args, m.ID = name, args, id
	return nil
}

// MarshalJSON encodes a Response as an Invocation tuple.
func (r Response) MarshalJSON() ([]byte, error) {
	args := r.Args
	if args == nil {
		args = map[string]interface{}{}
	}
	return json.Marshal([]interface{}{r.Name, args, r.ID})
}

// UnmarshalJSON decodes an Invocation tuple into a Response.
func (r *Response) UnmarshalJSON(data []byte) error {
	name, args, id, err := decodeInvocation(data)
	if err != nil {
		return err
	}
	r.Name, r.Args, r.ID = name, args, id
	return nil
}

// decodeInvocation parses a JMAP Invocation tuple [ name, arguments, methodCallId ].
func decodeInvocation(data []byte) (name string, args map[string]interface{}, id string, err error) {
	var tuple []json.RawMessage
	if err = json.Unmarshal(data, &tuple); err != nil {
		return "", nil, "", err
	}
	if len(tuple) != 3 {
		return "", nil, "", fmt.Errorf("jmap: invocation must be a 3-element array, got %d", len(tuple))
	}
	if err = json.Unmarshal(tuple[0], &name); err != nil {
		return "", nil, "", fmt.Errorf("jmap: invocation name: %w", err)
	}
	if err = json.Unmarshal(tuple[1], &args); err != nil {
		return "", nil, "", fmt.Errorf("jmap: invocation arguments: %w", err)
	}
	if err = json.Unmarshal(tuple[2], &id); err != nil {
		return "", nil, "", fmt.Errorf("jmap: invocation methodCallId: %w", err)
	}
	return name, args, id, nil
}
