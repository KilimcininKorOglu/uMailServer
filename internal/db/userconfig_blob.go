package db

// UserConfigBlob is the protocol-opaque Outlook EWS UserConfiguration payload —
// a dictionary blob, an XML blob, and a base64 binary blob. It is the typed
// value the relational backend stores in ews_user_config and the shape the EWS
// handler exchanges with the store, so neither side passes an untyped map.
//
// The JSON tags match the encoding the bbolt store used for the EWS handler's
// own struct, so the typed bbolt methods round-trip data written before this
// type existed.
type UserConfigBlob struct {
	Dictionary string `json:"dictionary,omitempty"`
	XMLData    string `json:"xml_data,omitempty"`
	BinaryData string `json:"binary_data,omitempty"`
}

// Category is a webmail message category (name + display color). It is the typed
// element of the categories preference, stored relationally as user_categories
// rows and, on bbolt, as the JSON the webmail handler used.
type Category struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}
