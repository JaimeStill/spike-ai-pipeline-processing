//go:build linux && (integration || findings)

package dispatch_test

// The part events the integration tier's route log and the formats finding both place: each
// part's event with where it sits below its input.

// tierEvent is a part's own event as the tier logs it: the input it came from, under the
// repository's root, its place, each Where from the input's child down to it joined by " > ",
// "-" for the input, and the event.
type tierEvent struct {
	input string
	place string
	ev    dispatched
}

// places returns events, one input's parts' events in the post-order Dispatch yields them, with
// each part's place: a part's parent's event is the first after it one level up.
func places(input string, events []dispatched) []tierEvent {
	out := make([]tierEvent, len(events))
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		place := "-"
		if ev.Part.Depth > 0 {
			place = ev.Part.Where
			for j := i + 1; j < len(events); j++ {
				if events[j].Part.Depth == ev.Part.Depth-1 {
					if out[j].place != "-" {
						place = out[j].place + " > " + place
					}
					break
				}
			}
		}
		out[i] = tierEvent{input, place, ev}
	}
	return out
}
