package db

// UserConfigBlob is the protocol-opaque Outlook EWS UserConfiguration payload —
// a dictionary blob, an XML blob, and a base64 binary blob. It is the typed
// value the relational backend stores in ews_user_config and the shape the EWS
// handler exchanges with the store, so neither side passes an untyped map.
type UserConfigBlob struct {
	Dictionary string
	XMLData    string
	BinaryData string
}
