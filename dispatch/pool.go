package dispatch

import (
	"context"
	"fmt"
	"iter"
	"sync"
)

// PoolEvent is one event of a pool's dispatch: the input it belongs to, as the pool was given
// it, and the event of one of its parts.
type PoolEvent struct {
	Input Input
	Event Event
}

// poolItem is what a worker hands the caller's loop: an input's event or the error that stopped
// its dispatch, and the channel the loop answers on once its body has returned, true to go on.
type poolItem struct {
	ev   PoolEvent
	err  error
	done chan bool
}

// Pool dispatches each of inputs, as [Dispatcher.Dispatch] does, over workers dispatches at once,
// all sharing the dispatcher's index, so content two inputs share is prepared once, and yields
// every input's events as one sequence, each with its input. An input's events come in its own
// dispatch's order; the inputs' interleave. inputs is ranged over on a goroutine of the pool's
// own, one input at a time as a worker is free, and the caller's loop body runs on the caller's
// goroutine alone.
//
// Each worker waits for the caller's loop body to return before its dispatch goes on, so the
// dispatcher's contract holds as it does for one dispatch: an output is appended to its part's
// pending entry as the body left it, its Ref set and [Event.Keep] called, the part's entry is
// recorded, and its files are removed, only once the body has returned. A worker holds at most
// one event the loop hasn't taken, so a slow loop holds every worker back, and the disk holds at
// most what workers dispatches hold, the files of the parts each has in flight.
//
// An error that stops one input's dispatch, its input unreadable or the index failing, is
// yielded with that input, its Event zero, and the other inputs go on. Once ctx ends, the pool
// takes no more inputs, its dispatches stop, and it yields ctx's error alone, last, in place of
// each dispatch's own. A loop that stops early stops every dispatch, as Dispatch's does, and the
// pool returns once every worker has, its claims released and its files removed. workers under 1
// is an error, yielded alone.
func (d *Dispatcher) Pool(ctx context.Context, inputs iter.Seq[Input], workers int) iter.Seq2[PoolEvent, error] {
	return func(yield func(PoolEvent, error) bool) {
		if workers < 1 {
			yield(PoolEvent{}, fmt.Errorf("dispatch: pool: %d workers, want at least 1", workers))
			return
		}
		// pctx ends the dispatches when the loop stops early; stop tells the feeder and the
		// workers to hand over nothing more.
		pctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := make(chan struct{})
		jobs := make(chan Input)
		items := make(chan poolItem)

		var feeder, pool sync.WaitGroup
		feeder.Go(func() {
			defer close(jobs)
			for in := range inputs {
				select {
				case jobs <- in:
				case <-stop:
					return
				case <-pctx.Done():
					return
				}
			}
		})
		for range workers {
			pool.Go(func() {
				for in := range jobs {
					if !d.poolDispatch(pctx, in, items, stop) {
						return
					}
				}
			})
		}
		go func() {
			pool.Wait()
			close(items)
		}()

		stopped := false
		for it := range items {
			if stopped {
				it.done <- false
				continue
			}
			goOn := true
			switch {
			case it.err != nil && ctx.Err() != nil:
				// The dispatch stopped because ctx ended: the pool's own error, last, stands for it.
			case it.err != nil:
				goOn = yield(PoolEvent{Input: it.ev.Input}, it.err)
			default:
				goOn = yield(it.ev, nil)
			}
			it.done <- goOn
			if !goOn {
				stopped = true
				close(stop)
				cancel()
			}
		}
		feeder.Wait()
		if !stopped && ctx.Err() != nil {
			yield(PoolEvent{}, ctx.Err())
		}
	}
}

// poolDispatch dispatches in, handing each event, and the error that stops it, to the pool's
// loop through items, and waiting for the loop's answer before going on. It returns false once
// the loop has stopped or stop is closed.
func (d *Dispatcher) poolDispatch(ctx context.Context, in Input, items chan<- poolItem, stop <-chan struct{}) bool {
	for ev, err := range d.Dispatch(ctx, in) {
		it := poolItem{ev: PoolEvent{Input: in, Event: ev}, err: err, done: make(chan bool, 1)}
		select {
		case items <- it:
		case <-stop:
			return false
		}
		if !<-it.done {
			return false
		}
	}
	return true
}
