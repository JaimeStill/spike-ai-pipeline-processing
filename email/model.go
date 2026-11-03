//go:build linux

package email

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"
)

// Schema is the version of mail's JSON Lines the package decodes. A read whose store line gives
// any other schema is refused with ErrSchema, since a new schema version is a change to the
// lines' shape.
const Schema = "1"

// ErrSchema is the cause of the error a read returns for a store line whose schema isn't Schema;
// the error names the schema the line gave.
var ErrSchema = errors.New("unsupported schema")

// Record is one line of mail's output, by its kind: *Store, *Folder, *Message, or *Skipped, and
// *UnknownRecord for a kind schema "1" adds later. Line returns the line's "line" field.
//
// Each record type mirrors its line: a field the line leaves out is the zero value, which means
// none, as mail's model means it; an optional number whose zero is meaningful is a pointer, nil
// for none. Each marshals back, through encoding/json/v2, to its line's fields less "line" and
// any false boolean.
type Record interface {
	Line() string
	record()
}

// Store is the store line, the first of every read: the input's format and, for a .pst, its
// encoding, whether it is password-protected, and its display name.
type Store struct {
	// Schema is the lines' version, Schema for every Store a read yields.
	Schema string `json:"schema,omitzero"`
	// Format is the input's format, sniffed from its content: "pst" or "msg".
	Format string `json:"format,omitzero"`
	// Encoding is a .pst's: "unicode" or "ansi".
	Encoding string `json:"encoding,omitzero"`
	// Password is whether a .pst is password-protected.
	Password bool `json:"password,omitzero"`
	// DisplayName is a .pst's display name.
	DisplayName string `json:"display_name,omitzero"`
}

// Folder is a folder line of a .pst, which comes before its messages' lines and they before its
// subfolders'.
type Folder struct {
	// ID is the folder's node id.
	ID uint64 `json:"id,omitzero"`
	// Parent is its parent folder's node id; zero for the IPM subtree's root, which has none.
	Parent uint64 `json:"parent,omitzero"`
	// Path is the display names from the IPM subtree's root down to this folder.
	Path []string `json:"path,omitzero"`
	// DisplayName is its display name.
	DisplayName string `json:"display_name,omitzero"`
	// ContainerClass is its container class, such as IPF.Note.
	ContainerClass string `json:"container_class,omitzero"`
}

// Message is a message line, whatever its message class, or a message nested under a .msg's
// attachment record. mail writes no class-specific field, and groups nothing into threads.
type Message struct {
	// ID is, in a .pst, the message's node id.
	ID uint64 `json:"id,omitzero"`
	// Folder is, in a .pst, the node id of its folder.
	Folder uint64 `json:"folder,omitzero"`
	// Class is the message class, such as IPM.Note.
	Class string `json:"class,omitzero"`
	// Subject is the subject, its prefix marker dropped.
	Subject string `json:"subject,omitzero"`
	// Sender is the sender's address.
	Sender Address `json:"sender,omitzero"`
	// To, CC, and BCC are the recipients' addresses, in the recipient table's order.
	To  []Address `json:"to,omitzero"`
	CC  []Address `json:"cc,omitzero"`
	BCC []Address `json:"bcc,omitzero"`
	// SubmitTime, DeliveryTime, CreationTime, and ModificationTime are UTC, to the second.
	SubmitTime       time.Time `json:"submit_time,omitzero"`
	DeliveryTime     time.Time `json:"delivery_time,omitzero"`
	CreationTime     time.Time `json:"creation_time,omitzero"`
	ModificationTime time.Time `json:"modification_time,omitzero"`
	// Importance is "low", "normal", or "high".
	Importance string `json:"importance,omitzero"`
	// Sensitivity is "normal", "personal", "private", or "confidential".
	Sensitivity string `json:"sensitivity,omitzero"`
	// TransportHeaders are the raw transport headers, as stored.
	TransportHeaders string `json:"transport_headers,omitzero"`
	// MessageID, InReplyTo, and References are the Internet threading fields, as stored.
	MessageID  string `json:"message_id,omitzero"`
	InReplyTo  string `json:"in_reply_to,omitzero"`
	References string `json:"references,omitzero"`
	// ConversationTopic is the conversation topic, as stored.
	ConversationTopic string `json:"conversation_topic,omitzero"`
	// ConversationIndex is the lowercase hex of the stored conversation index's bytes.
	ConversationIndex string `json:"conversation_index,omitzero"`
	// Body is the message's bodies, stored and derived.
	Body Body `json:"body,omitzero"`
	// Attachments are its attachment records, in the attachment table's order.
	Attachments []Attachment `json:"attachments,omitzero"`
}

// Address is a sender's or recipient's address, each field as stored.
type Address struct {
	Name    string `json:"name,omitzero"`
	Address string `json:"address,omitzero"`
	// AddressType is the address's type, such as SMTP or EX; an EX address is its X.500
	// distinguished name.
	AddressType string `json:"address_type,omitzero"`
}

// Body is a message's bodies: those stored, each as stored, and mail's own under Derived, so a
// stored and a derived body never share a place.
type Body struct {
	// Text is the plain-text body.
	Text string `json:"text,omitzero"`
	// HTML is the HTML body, decoded by mail as its README says.
	HTML string `json:"html,omitzero"`
	// RTF is the RTF body, decompressed.
	RTF string `json:"rtf,omitzero"`
	// Derived is what mail made from the stored bodies.
	Derived Derived `json:"derived,omitzero"`
}

// Derived is the bodies mail makes from a message's stored ones.
type Derived struct {
	// HTML is the HTML recovered from an RTF body that encapsulates HTML.
	HTML string `json:"html,omitzero"`
	// Markdown is made from the stored HTML, else from the HTML recovered from RTF, else from the
	// plain text. Its images keep their cid: sources, which name attachments' ContentID.
	Markdown string `json:"markdown,omitzero"`
}

// Attachment is one of a message's attachment records.
type Attachment struct {
	// File is the name of its content's file: the lowercase hex SHA-256 of the bytes, plus "."
	// and the stored extension when that is 1 to 8 ASCII letters and digits. "" for an
	// attachment whose content isn't a file.
	File string `json:"file,omitzero"`
	// Filename is the long filename, else the short one.
	Filename string `json:"filename,omitzero"`
	// MimeType is the MIME type, as stored.
	MimeType string `json:"mime_type,omitzero"`
	// ContentID is the content id, as stored, without angle brackets.
	ContentID string `json:"content_id,omitzero"`
	// Inline is whether the HTML body refers to it, how an inline disposition is stored.
	Inline bool `json:"inline,omitzero"`
	// Method is the attach method: "none", "by_value", "by_reference", "by_reference_only",
	// "embedded_message", "storage", or "by_web_reference".
	Method string `json:"method,omitzero"`
	// Size is the content's size in bytes, nil when its content wasn't read.
	Size *int64 `json:"size,omitzero"`
	// Message is, in a .msg, an embedded message, nested with its own bodies and attachments; nil
	// otherwise. In a .pst an embedded message is listed with its metadata alone.
	Message *Message `json:"message,omitzero"`
}

// Skipped is a skipped line: a record mail couldn't read inside a readable store, in the place
// the record would have had.
type Skipped struct {
	// Kind is "folder", "message", or "attachment"; or "contents" or "hierarchy" for a folder's
	// table.
	Kind string `json:"kind,omitzero"`
	// Folder is the node id of the folder the record is in: a skipped folder names its parent,
	// and a skipped table the folder whose table it is.
	Folder uint64 `json:"folder,omitzero"`
	// Message is an attachment's message's node id.
	Message uint64 `json:"message,omitzero"`
	// ID is the record's own id, nil for a table: in a .pst, its node id, an attachment's within
	// its message; in a .msg, an attachment's number, which may be zero.
	ID *uint64 `json:"id,omitzero"`
	// Reason is why it couldn't be read.
	Reason string `json:"reason,omitzero"`
}

// UnknownRecord is a line of a kind the package doesn't know, kept with its "line" and its JSON,
// never an error, since mail's schema grows by adding kinds.
type UnknownRecord struct {
	// Kind is the line's "line" field.
	Kind string
	// Raw is the line's JSON, a copy the record owns.
	Raw jsontext.Value
}

// Line returns "store".
func (*Store) Line() string { return "store" }

// Line returns "folder".
func (*Folder) Line() string { return "folder" }

// Line returns "message".
func (*Message) Line() string { return "message" }

// Line returns "skipped".
func (*Skipped) Line() string { return "skipped" }

// Line returns the line's kind, as it gave it.
func (r *UnknownRecord) Line() string { return r.Kind }

func (*Store) record()         {}
func (*Folder) record()        {}
func (*Message) record()       {}
func (*Skipped) record()       {}
func (*UnknownRecord) record() {}

// lineHead is what decodeLine reads of a line before its kind's type.
type lineHead struct {
	Line   *string `json:"line"`
	Schema *string `json:"schema"`
}

// decodeLine decodes one line, raw, into its record. first says whether it is the read's first
// line, which must be the store line, whose schema must be Schema. raw is the decoder's buffer,
// valid only until its next read, so an UnknownRecord keeps a copy.
func decodeLine(raw jsontext.Value, first bool) (Record, error) {
	var head lineHead
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("email: decoding a line: %w", err)
	}
	if head.Line == nil {
		return nil, errors.New("email: a line with no line field")
	}
	kind := *head.Line
	if first {
		if kind != "store" {
			return nil, fmt.Errorf("email: the first line is a %s line, want the store line", kind)
		}
		switch {
		case head.Schema == nil:
			return nil, fmt.Errorf("email: %w: the store line has none, want %q", ErrSchema, Schema)
		case *head.Schema != Schema:
			return nil, fmt.Errorf("email: %w %q, want %q", ErrSchema, *head.Schema, Schema)
		}
	} else if kind == "store" {
		return nil, errors.New("email: a second store line")
	}
	var rec Record
	switch kind {
	case "store":
		rec = new(Store)
	case "folder":
		rec = new(Folder)
	case "message":
		rec = new(Message)
	case "skipped":
		rec = new(Skipped)
	default:
		return &UnknownRecord{Kind: kind, Raw: raw.Clone()}, nil
	}
	if err := json.Unmarshal(raw, rec); err != nil {
		return nil, fmt.Errorf("email: decoding a %s line: %w", kind, err)
	}
	return rec, nil
}
