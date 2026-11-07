//go:build linux

package dispatch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
)

// EmailConfig is what an [EmailStage] is built over: the caller's Reader and the version of the
// tool behind it.
type EmailConfig struct {
	// Reader reads the messages and stores; it is required.
	Reader *email.Reader
	// Version names mail's version, as [Versions] gives it; it is required.
	Version string
}

// EmailStage prepares a message or a mail store, route.Nested's parts, a .msg, an .eml, an mbox, or
// a .pst, through email's Reader, its attachments written into the part's directory. Each of mail's
// records is a [RecordOutput], its JSON as email's record marshals it, in mail's order: the store
// line, named "store"; each folder, "folder <id>", its Meta naming its id and path; each message,
// "message <id>", or "message" for a .msg's or an .eml's, which has none, then its derived Markdown
// body, when it has one, a [MarkdownOutput] of the same name, the Meta of both naming its id, its
// folder's path, its subject, and its Message-ID, and an embedded one's the message it is embedded
// in, and the record's Meta "markdown" saying "follows" when a Markdown follows it; each message of
// a conversation class, a Skype for Business conversation or missed conversation, as email reads
// it, an email.Conversation record, "conversation <id>", or "conversation" without one, then its
// transcript's Markdown, of the same name, in the same order, with the same Meta less the subject
// and Message-ID and with its topic and "missed", "true" or "false", an embedded one's too, since
// the class decides; each skipped record, "skipped <kind>", its Meta naming its reason; and a
// record of a kind email doesn't know, by its kind. mail's warnings, when it gave any, are a last
// record, "warnings", a JSON array.
//
// A message's attachments come before its own outputs, in their order: mail writes a message's
// attachments before its line, so each is a part dispatched, every part found in it with it,
// before the message's record and Markdown are yielded, and the message's last output, its
// Markdown or, with none, its record, is the last of everything the message holds. Each
// attachment whose content mail wrote is a [Found] part, the dispatcher's to route and inspect
// again, named by its filename and typed by its stored MIME type, its Where naming its message
// and its place, "message <id>, attachment <n>". An embedded message whose content is no file, as
// a .msg's or an .eml's is, mail giving no bytes of it, is prepared inline, as a message of its
// own, in its place among the attachments: its own attachments found below it, "message <id>,
// attachment <n>, attachment <m>", at any depth, then its record and Markdown, named by its place,
// "message <id>, attachment <n>". An attachment with neither, by reference, not read, or a .pst's
// embedded message listed by its metadata alone, is a record named by its place, its Meta naming
// why it has no content.
//
// A part with an After, an [Input.After], is read from the message after it, as email.Request's
// After says: the store, every folder, and the messages after it, which needs mail 0.3.0 or
// later; a .msg's or an .eml's read, or an After that names no message, fails.
type EmailStage struct {
	reader *email.Reader
	id     Identity
}

// NewEmailStage builds an EmailStage over cfg, refusing one with no Reader or Version.
func NewEmailStage(cfg EmailConfig) (*EmailStage, error) {
	if cfg.Reader == nil {
		return nil, errors.New("dispatch: email stage: no reader")
	}
	id, err := adapterIdentity("email", cfg.Version, struct {
		Schema string `json:"schema"`
	}{email.Schema})
	if err != nil {
		return nil, err
	}
	return &EmailStage{reader: cfg.Reader, id: id}, nil
}

// Identity returns the stage's identity: "email", mail's version, and the schema's hash.
func (s *EmailStage) Identity() Identity { return s.id }

// Prepare reads the message or store at p.Path, as the type's doc says.
func (s *EmailStage) Prepare(ctx context.Context, p Part, dir string) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		attachments := filepath.Join(dir, "attachments")
		if err := os.Mkdir(attachments, 0o700); err != nil {
			yield(Item{}, fmt.Errorf("email stage: %w", err))
			return
		}
		stream, err := s.reader.Read(ctx, email.Request{Input: p.Path, Attachments: attachments, After: p.After})
		if err != nil {
			yield(Item{}, err)
			return
		}
		w := emailWalk{stash: stash{dir: dir}, folders: map[uint64][]string{}, yield: yield}
		for rec, err := range stream.Records() {
			if err != nil {
				yield(Item{}, err)
				return
			}
			if err := w.record(rec); err != nil {
				if !errors.Is(err, errStopped) {
					yield(Item{}, err)
				}
				return
			}
		}
		if warnings := stream.Warnings(); len(warnings) > 0 {
			if err := w.output(record("warnings", warnings, nil)); err != nil && !errors.Is(err, errStopped) {
				yield(Item{}, err)
			}
		}
	}
}

// errStopped is an adapter walk's word that the caller's loop stopped: nothing more is yielded.
var errStopped = errors.New("stopped")

// emailWalk is one read's walk of its records: its stash, its folders' paths by id, and the
// loop's yield.
type emailWalk struct {
	stash   stash
	folders map[uint64][]string
	yield   func(Item, error) bool
}

// output yields o, or returns err, record's error, or errStopped once the loop stops.
func (w *emailWalk) output(o Output, err error) error {
	if err != nil {
		return err
	}
	if !w.yield(OutputItem(o), nil) {
		return errStopped
	}
	return nil
}

// record yields rec's outputs and parts.
func (w *emailWalk) record(rec email.Record) error {
	switch r := rec.(type) {
	case *email.Store:
		return w.output(record("store", r, map[string]string{"format": r.Format}))
	case *email.Folder:
		w.folders[r.ID] = r.Path
		id := strconv.FormatUint(r.ID, 10)
		return w.output(record("folder "+id, r, map[string]string{"id": id, "path": strings.Join(r.Path, "/")}))
	case *email.Message:
		name := "message"
		if r.ID != 0 {
			name += " " + strconv.FormatUint(r.ID, 10)
		}
		return w.message(r, name, "")
	case *email.Conversation:
		name := "conversation"
		if r.ID != 0 {
			name += " " + strconv.FormatUint(r.ID, 10)
		}
		return w.conversation(r, name, "")
	case *email.Skipped:
		return w.output(record("skipped "+r.Kind, r, map[string]string{"reason": r.Reason}))
	case *email.UnknownRecord:
		return w.output(Output{Kind: RecordOutput, Name: r.Kind, MediaType: "application/json", Value: r.Raw}, nil)
	}
	return fmt.Errorf("email stage: a record of type %T", rec)
}

// message yields m's attachments, then its record and Markdown, named name: each attachment's
// file found, each embedded message without one prepared inline below name, and each other a
// record. parent is the name of the message m is embedded in, "" for a message of the read's own.
// Its own outputs come last, so a caller that has persisted them has had every part of m.
func (w *emailWalk) message(m *email.Message, name, parent string) error {
	if err := w.attachments(m.Attachments, name); err != nil {
		return err
	}
	meta := map[string]string{"subject": m.Subject}
	if m.MessageID != "" {
		meta["message_id"] = m.MessageID
	}
	return w.own(m, name, parent, m.ID, m.Folder, m.Body.Derived.Markdown, meta)
}

// conversation yields c's attachments, then its record and its transcript's Markdown, named name,
// as message yields a message's; the Meta of both names its topic and whether it was missed.
func (w *emailWalk) conversation(c *email.Conversation, name, parent string) error {
	if err := w.attachments(c.Attachments, name); err != nil {
		return err
	}
	meta := map[string]string{"topic": c.Topic, "missed": strconv.FormatBool(c.Missed)}
	return w.own(c, name, parent, c.ID, c.Folder, c.Markdown, meta)
}

// attachments yields each of a message's attachments, in their order, the message named name.
func (w *emailWalk) attachments(as []email.Attachment, name string) error {
	for i := range as {
		where := name + ", attachment " + strconv.Itoa(i+1)
		if err := w.attachment(&as[i], where, name); err != nil {
			return err
		}
	}
	return nil
}

// own yields a message's own outputs, named name: v, its record, then md, its Markdown, when it
// has one. meta gains its id, its folder's path, and parent, the message it is embedded in, when
// it has each; the record's says "markdown" "follows" when a Markdown follows it, so a caller
// knows which output is the message's last.
func (w *emailWalk) own(v any, name, parent string, id, folder uint64, md string, meta map[string]string) error {
	if id != 0 {
		meta["id"] = strconv.FormatUint(id, 10)
	}
	if path, ok := w.folders[folder]; ok && folder != 0 {
		meta["folder"] = strings.Join(path, "/")
	}
	if parent != "" {
		meta["embedded_in"] = parent
	}
	recMeta := meta
	if md != "" {
		recMeta = maps.Clone(meta)
		recMeta["markdown"] = "follows"
	}
	if err := w.output(record(name, v, recMeta)); err != nil {
		return err
	}
	if md != "" {
		if err := w.output(markdown(name, md, meta), nil); err != nil {
			return err
		}
	}
	return nil
}

// attachment yields a, at where in the message named parent, as message says.
func (w *emailWalk) attachment(a *email.Attachment, where, parent string) error {
	if a.Path != "" {
		f, ok, err := w.stash.find(a.Path, Found{Name: cmp.Or(a.Filename, a.File), MIME: a.MimeType, Where: where})
		if err != nil {
			return err
		}
		if ok {
			if !w.yield(PartItem(f), nil) {
				return errStopped
			}
			return nil
		}
	}
	if a.Message != nil && a.File == "" {
		// The class decides, nested or not: an embedded message of a conversation class is read
		// as the Stream reads one of the store's own.
		if c, ok := a.Message.Conversation(); ok {
			return w.conversation(c, where, parent)
		}
		return w.message(a.Message, where, parent)
	}
	_, why, err := a.Route()
	if err != nil {
		return fmt.Errorf("email stage: %s: %w", where, err)
	}
	return w.output(record(where, a, map[string]string{"reason": why, "filename": a.Filename}))
}
