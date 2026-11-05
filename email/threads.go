//go:build linux

package email

import (
	"cmp"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"mime"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Thread is one message of a thread, or a placeholder for a message the store doesn't hold, with
// its replies beneath it. Threads and Threader.Threads return the roots, each a thread.
//
// Roots and each set of children are ordered by sent date, oldest first: a message's submit
// time, else its delivery time, else none, which sorts first; a placeholder takes its first
// child's. Messages of the same date are ordered by store id, then by their order in the read, so
// the same read always gives the same threads.
type Thread struct {
	// ID is the message's store id, its Message.ID: in a .pst, its node id; in an mbox, its
	// ordinal in the file. It is zero for a placeholder, and for a .msg's or an .eml's message,
	// which has none.
	ID uint64
	// Placeholder says the store doesn't hold this message: it stands for a Message-ID other
	// messages refer to, for the parent a conversation index names, or for the base subject
	// that gathers its children.
	Placeholder bool
	// MessageID is the message's own Message-ID, angle brackets included, or the one a
	// placeholder stands for; "" for a message without a valid one, and for a placeholder that
	// stands for a conversation index's parent or a subject.
	MessageID string
	// Children are its replies.
	Children []*Thread
}

// Threader builds a store's threads from the threading keys of its messages, fed to it one
// record at a time as a Stream's Records yields them. It keeps each message's keys alone, never
// its bodies or attachments, so its memory follows the number of messages and the size of their
// keys. The zero Threader is ready to use; it is used by one goroutine.
type Threader struct {
	keys []threadKey
}

// threadKey is what threading needs of one message.
type threadKey struct {
	// seq is the message's place in the read, which settles a tie of date and store id.
	seq  int
	id   uint64
	date time.Time
	// subject is the base subject, folded for comparison; replyFwd says extracting it removed a
	// reply or forward marker.
	subject  string
	replyFwd bool
	// messageID is the first valid Message-ID of the Message-ID field, "" for none.
	messageID string
	// refs are the message's references: References' valid Message-IDs, else In-Reply-To's
	// first.
	refs []string
	// index is the conversation index's bytes, "" when absent or malformed.
	index string
}

// Threads ranges over records, a Stream's Records, and returns the threads of their messages,
// as a Threader fed each record would. It returns the error that ends records, if any, and no
// threads.
func Threads(records iter.Seq2[Record, error]) ([]*Thread, error) {
	var t Threader
	for rec, err := range records {
		if err != nil {
			return nil, err
		}
		t.Add(rec)
	}
	return t.Threads(), nil
}

// Add takes rec's threading keys when it is a *Message, read from the fields mail stores: its
// store id, its sent date, its subject, its Message-ID, In-Reply-To, and References, and its
// conversation index. Any other record is ignored, as are messages nested under a .msg's
// attachment records, which aren't messages of the store.
func (t *Threader) Add(rec Record) {
	m, ok := rec.(*Message)
	if !ok {
		return
	}
	k := threadKey{seq: len(t.keys), id: m.ID, date: m.SubmitTime}
	if k.date.IsZero() {
		k.date = m.DeliveryTime
	}
	k.subject, k.replyFwd = baseSubject(m.Subject)
	k.subject = asciiUpper(k.subject)
	if ids := messageIDs(m.MessageID); len(ids) > 0 {
		k.messageID = ids[0]
	}
	k.refs = messageIDs(m.References)
	if len(k.refs) == 0 {
		if ids := messageIDs(m.InReplyTo); len(ids) > 0 {
			k.refs = ids[:1]
		}
	}
	if b, err := hex.DecodeString(m.ConversationIndex); err == nil && validIndex(len(b)) {
		k.index = string(b)
	}
	t.keys = append(t.keys, k)
}

// The conversation index's layout, MS-OXOMSG 2.2.1.3: a 22-byte header, then one 5-byte child
// block per reply.
const (
	indexHeader = 22
	indexBlock  = 5
)

// validIndex reports whether n bytes is a conversation index's length: the header and whole
// child blocks.
func validIndex(n int) bool {
	return n >= indexHeader && (n-indexHeader)%indexBlock == 0
}

// container is a node of the thread tree as RFC 5256's REFERENCES algorithm builds it: a
// message's, or a dummy's, whose key is nil.
type container struct {
	key *threadKey
	// messageID is the Message-ID a dummy stands for.
	messageID string
	parent    *container
	children  []*container
}

// Threads returns the threads of the messages added so far, by RFC 5256's REFERENCES algorithm
// over their Message-ID, In-Reply-To, and References, with one addition: a message whose
// headers name no parent takes as parent the message whose conversation index is its own less
// the last child block. It can be called again after more are added.
//
// Following RFC 5256: a message's references are its References' Message-IDs, else the first of
// its In-Reply-To's; a message without a valid Message-ID, or with one an earlier message has,
// is linked only by its own references; a Message-ID no message has is a placeholder; a link
// that would make a loop isn't made; a placeholder is removed and its children promoted, except
// a root's of several children; and roots with the same base subject are gathered, a reply or
// forward under the message it replies to. A message whose conversation index names a parent
// the store doesn't hold is linked to a placeholder for it, as a missing Message-ID is. A
// conversation index of any length but the header's and whole child blocks is ignored.
func (t *Threader) Threads() []*Thread {
	byID := map[string]*container{}
	byIndex := map[string]*container{}
	msgs := make([]*container, len(t.keys))
	var dummies []*container
	// Step 1's Message-ID rules: the first message with an ID keeps it, and any other is
	// linked by its own references alone. Conversation indexes are registered the same way.
	for i := range t.keys {
		k := &t.keys[i]
		c := &container{key: k}
		msgs[i] = c
		if k.messageID != "" && byID[k.messageID] == nil {
			byID[k.messageID] = c
		}
		if k.index != "" && byIndex[k.index] == nil {
			byIndex[k.index] = c
		}
	}
	// lookup returns the container of id in table, making a dummy for it when none has it, which
	// every later reference to it shares.
	lookup := func(table map[string]*container, id, messageID string) *container {
		if c := table[id]; c != nil {
			return c
		}
		c := &container{messageID: messageID}
		table[id] = c
		dummies = append(dummies, c)
		return c
	}
	for _, c := range msgs {
		refs := c.key.refs
		// Step 1.A: each reference the parent of the next, unless the next has a parent or the
		// link would make a loop.
		for i := 0; i+1 < len(refs); i++ {
			parent, child := lookup(byID, refs[i], refs[i]), lookup(byID, refs[i+1], refs[i+1])
			if child.parent == nil && !makesLoop(parent, child) {
				link(parent, child)
			}
		}
		// Step 1.B: the last reference is the message's parent, replacing any it has; with no
		// reference, the parent its conversation index names, else none.
		var parent *container
		if len(refs) > 0 {
			parent = lookup(byID, refs[len(refs)-1], refs[len(refs)-1])
		} else if idx := c.key.index; len(idx) > indexHeader {
			parent = lookup(byIndex, idx[:len(idx)-indexBlock], "")
		}
		if c.parent != parent {
			unlink(c)
			if parent != nil && !makesLoop(parent, c) {
				link(parent, c)
			}
		}
	}
	// Step 2: the containers without a parent are the roots.
	var roots []*container
	for _, c := range slices.Concat(msgs, dummies) {
		if c.parent == nil {
			roots = append(roots, c)
		}
	}
	// Step 3, 4, 5, and 6.
	roots = prune(roots, true)
	sortTree(roots)
	roots = gather(roots)
	sortTree(roots)
	threads := make([]*Thread, len(roots))
	for i, c := range roots {
		threads[i] = c.thread()
	}
	return threads
}

// makesLoop reports whether making child a child of parent would make a loop: whether child is
// parent or one of its ancestors.
func makesLoop(parent, child *container) bool {
	for p := parent; p != nil; p = p.parent {
		if p == child {
			return true
		}
	}
	return false
}

// link makes child, which has no parent, the last child of parent.
func link(parent, child *container) {
	child.parent = parent
	parent.children = append(parent.children, child)
}

// unlink removes c from its parent's children, if it has a parent.
func unlink(c *container) {
	if p := c.parent; p != nil {
		p.children = slices.DeleteFunc(p.children, func(x *container) bool { return x == c })
		c.parent = nil
	}
}

// prune is step 3 over list, the roots when atRoot: it removes each dummy without children and
// promotes a dummy's children to its own level, unless that would make several roots of them.
func prune(list []*container, atRoot bool) []*container {
	var out []*container
	for _, c := range list {
		c.children = prune(c.children, false)
		if c.key == nil {
			if len(c.children) == 0 {
				continue
			}
			if !atRoot || len(c.children) == 1 {
				for _, child := range c.children {
					child.parent = c.parent
				}
				out = append(out, c.children...)
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// sortKey returns the key c sorts by: its message's, or a dummy's first child's.
func (c *container) sortKey() *threadKey {
	for c.key == nil {
		c = c.children[0]
	}
	return c.key
}

// sortTree sorts every set of siblings under list, and list, by sent date, then store id, then
// order in the read, the deepest first so a dummy sorts by its first child: steps 4 and 6.
func sortTree(list []*container) {
	for _, c := range list {
		sortTree(c.children)
	}
	slices.SortFunc(list, func(a, b *container) int {
		ka, kb := a.sortKey(), b.sortKey()
		return cmp.Or(ka.date.Compare(kb.date), cmp.Compare(ka.id, kb.id), cmp.Compare(ka.seq, kb.seq))
	})
}

// gather is step 5 over roots: it gathers the roots that share a base subject.
func gather(roots []*container) []*container {
	// threadSubject returns a root's base subject, a dummy's first child's.
	threadSubject := func(c *container) string { return c.sortKey().subject }
	// Step 5.B: one root per subject, preferring a dummy, then a message that isn't a reply or
	// forward.
	table := map[string]*container{}
	for _, c := range roots {
		subject := threadSubject(c)
		if subject == "" {
			continue
		}
		have := table[subject]
		if have == nil || have.key != nil && (c.key == nil || have.key.replyFwd && !c.key.replyFwd) {
			table[subject] = c
		}
	}
	// Step 5.C: merge each other root into its subject's.
	removed := map[*container]bool{}
	replaced := map[*container]*container{}
	for _, c := range roots {
		subject := threadSubject(c)
		if subject == "" {
			continue
		}
		have := table[subject]
		if have == c {
			continue
		}
		removed[c] = true
		switch {
		case have.key == nil && c.key == nil:
			for _, child := range c.children {
				child.parent = have
			}
			have.children = append(have.children, c.children...)
			c.children = nil
		case have.key == nil, c.key.replyFwd && !have.key.replyFwd:
			link(have, c)
		default:
			dummy := &container{}
			replaced[have] = dummy
			link(dummy, have)
			link(dummy, c)
			table[subject] = dummy
		}
	}
	var out []*container
	for _, c := range roots {
		if removed[c] {
			continue
		}
		if r := replaced[c]; r != nil {
			c = r
		}
		out = append(out, c)
	}
	return out
}

// thread returns c and its children as a Thread.
func (c *container) thread() *Thread {
	t := &Thread{Placeholder: c.key == nil, MessageID: c.messageID}
	if c.key != nil {
		t.ID, t.MessageID = c.key.id, c.key.messageID
	}
	for _, child := range c.children {
		t.Children = append(t.Children, child.thread())
	}
	return t
}

// msgIDPattern matches a Message-ID in angle brackets.
var msgIDPattern = regexp.MustCompile(`<([^<>\s]*)>`)

// messageIDs returns the valid Message-IDs of a Message-ID, In-Reply-To, or References field,
// each in angle brackets, in order. A valid one has an "@", as RFC 5322's msg-id does. A field
// without any angle bracket is read as Message-IDs without them, separated by white space, as
// some writers store them.
func messageIDs(field string) []string {
	var ids []string
	if !strings.ContainsAny(field, "<>") {
		for id := range strings.FieldsSeq(field) {
			if strings.Contains(id, "@") {
				ids = append(ids, "<"+id+">")
			}
		}
		return ids
	}
	for _, m := range msgIDPattern.FindAllStringSubmatch(field, -1) {
		if strings.Contains(m[1], "@") {
			ids = append(ids, m[0])
		}
	}
	return ids
}

// RFC 5256's subject grammar, section 5, case-insensitive: a subj-blob, and a subj-leader that
// is a subj-refwd after any subj-blobs; and runs of spaces, which extraction makes one.
var (
	subjBlob  = regexp.MustCompile(`^\[[^\[\]]*\][ \t]*`)
	subjRefwd = regexp.MustCompile(`(?i)^(?:\[[^\[\]]*\][ \t]*)*(?:re|fwd?)[ \t]*(?:\[[^\[\]]*\][ \t]*)?:`)
	spaces    = regexp.MustCompile(`  +`)
)

// subjFwdHdr is RFC 5256's subj-fwd-hdr, matched case-insensitively.
const subjFwdHdr = "[fwd:"

// baseSubject returns subject's base subject, by RFC 5256's section 2.1, and whether extracting
// it removed a subj-refwd, a "(fwd)" subj-trailer, or a subj-fwd-hdr and subj-fwd-trl: whether
// the message is a reply or forward.
func baseSubject(subject string) (base string, replyFwd bool) {
	// (1) Encoded-words decoded, where they can be; tabs and continuations made spaces, and runs
	// of spaces one.
	s := subject
	if decoded, err := new(mime.WordDecoder).DecodeHeader(s); err == nil {
		s = decoded
	}
	s = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ").Replace(s)
	s = spaces.ReplaceAllString(s, " ")
	for {
		// (2) Trailers: white space and "(fwd)".
		for {
			if t, ok := strings.CutSuffix(s, " "); ok {
				s = t
			} else if n := len(s) - len("(fwd)"); n >= 0 && strings.EqualFold(s[n:], "(fwd)") {
				s, replyFwd = s[:n], true
			} else {
				break
			}
		}
		// (5) Repeating (3) and (4).
		for {
			// (3) Leaders: white space and subj-refwd.
			for {
				if t, ok := strings.CutPrefix(s, " "); ok {
					s = t
				} else if loc := subjRefwd.FindStringIndex(s); loc != nil {
					s, replyFwd = s[loc[1]:], true
				} else {
					break
				}
			}
			// (4) A subj-blob, when what follows isn't empty.
			loc := subjBlob.FindStringIndex(s)
			if loc == nil || s[loc[1]:] == "" {
				break
			}
			s = s[loc[1]:]
		}
		// (6) A subj-fwd, its header and trailer removed, from (2) again.
		if len(s) > len(subjFwdHdr) && strings.EqualFold(s[:len(subjFwdHdr)], subjFwdHdr) && strings.HasSuffix(s, "]") {
			s, replyFwd = s[len(subjFwdHdr):len(s)-1], true
			continue
		}
		return s, replyFwd
	}
}

// asciiUpper returns s with its ASCII letters upper case, the i;ascii-casemap comparator's
// folding, under which RFC 5256 compares base subjects.
func asciiUpper(s string) string {
	return strings.Map(func(r rune) rune {
		if 'a' <= r && r <= 'z' {
			return r - 'a' + 'A'
		}
		return r
	}, s)
}

// threaderMagic begins a saved Threader: the format's name and its version, 1.
const threaderMagic = "email.Threader\x00\x01"

// MarshalBinary saves the Threader's keys, so a read resumed with Request.After threads whole: a
// Threader restored from them by UnmarshalBinary, then fed the resumed read's records, returns
// the threads one fed the whole read would. The format is versioned and deterministic, the same
// keys always giving the same bytes: threaderMagic, then the count of messages, then each
// message's keys in the order they were added, its store id, its sent date as time.Time's
// MarshalBinary gives it, its folded base subject, whether it is a reply or forward, its
// Message-ID, its references, and its conversation index; each number an unsigned varint, each
// string its length and its bytes. Its error is that of a date time.Time can't marshal.
func (t *Threader) MarshalBinary() ([]byte, error) {
	b := []byte(threaderMagic)
	b = binary.AppendUvarint(b, uint64(len(t.keys)))
	for i := range t.keys {
		k := &t.keys[i]
		b = binary.AppendUvarint(b, k.id)
		date, err := k.date.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("email: threader: message %d's date: %w", k.id, err)
		}
		b = appendString(b, string(date))
		b = appendString(b, k.subject)
		var replyFwd uint64
		if k.replyFwd {
			replyFwd = 1
		}
		b = binary.AppendUvarint(b, replyFwd)
		b = appendString(b, k.messageID)
		b = binary.AppendUvarint(b, uint64(len(k.refs)))
		for _, ref := range k.refs {
			b = appendString(b, ref)
		}
		b = appendString(b, k.index)
	}
	return b, nil
}

// UnmarshalBinary restores the keys MarshalBinary saved, replacing the Threader's own. It
// refuses data of another format or version, cut short, or with bytes after its last message,
// leaving the Threader as it was.
func (t *Threader) UnmarshalBinary(data []byte) error {
	r := keyReader{b: data}
	if !strings.HasPrefix(string(data), threaderMagic) {
		return errors.New("email: threader: not a saved Threader of version 1")
	}
	r.b = r.b[len(threaderMagic):]
	n := r.uvarint()
	// Each message takes at least seven bytes, so a count past that is no count of this data's.
	if r.err == nil && n > uint64(len(r.b))/7 {
		r.err = errors.New("its count of messages is past its length")
	}
	var keys []threadKey
	for i := uint64(0); i < n && r.err == nil; i++ {
		k := threadKey{seq: int(i), id: r.uvarint()}
		if err := k.date.UnmarshalBinary([]byte(r.string())); err != nil && r.err == nil {
			r.err = err
		}
		k.subject = r.string()
		switch r.uvarint() {
		case 0:
		case 1:
			k.replyFwd = true
		default:
			if r.err == nil {
				r.err = errors.New("a reply flag neither 0 nor 1")
			}
		}
		k.messageID = r.string()
		refs := r.uvarint()
		if r.err == nil && refs > uint64(len(r.b)) {
			r.err = errors.New("its count of references is past its length")
		}
		for j := uint64(0); j < refs && r.err == nil; j++ {
			k.refs = append(k.refs, r.string())
		}
		k.index = r.string()
		keys = append(keys, k)
	}
	if r.err == nil && len(r.b) > 0 {
		r.err = fmt.Errorf("%d bytes after its last message", len(r.b))
	}
	if r.err != nil {
		return fmt.Errorf("email: threader: %w", r.err)
	}
	t.keys = keys
	return nil
}

// appendString appends s to b as its length, an unsigned varint, and its bytes.
func appendString(b []byte, s string) []byte {
	return append(binary.AppendUvarint(b, uint64(len(s))), s...)
}

// keyReader reads a saved Threader's numbers and strings, keeping its first error, after which
// each read returns zero.
type keyReader struct {
	b   []byte
	err error
}

// uvarint reads an unsigned varint.
func (r *keyReader) uvarint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.err = errors.New("cut short")
		return 0
	}
	r.b = r.b[n:]
	return v
}

// string reads a string, its length and its bytes.
func (r *keyReader) string() string {
	n := r.uvarint()
	if r.err != nil {
		return ""
	}
	if n > uint64(len(r.b)) {
		r.err = errors.New("cut short")
		return ""
	}
	s := string(r.b[:n])
	r.b = r.b[n:]
	return s
}
