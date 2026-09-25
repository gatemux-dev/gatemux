package api

import (
	"context"
	"time"
)

type eventResult struct {
	event *sseEvent
	err   error
}

// Disabled by default: no extra goroutine or queue on the hot path. Enabled
// streams have exactly one demand-driven reader, one result, and one writer.
// close cancels and joins the reader even on a handler panic/client disconnect.
type eventPuller struct {
	src      *chatStream
	w        *streamWriter
	requests chan struct{}
	results  chan eventResult
	done     chan struct{}
	ticker   *time.Ticker
}

func newEventPuller(src *chatStream, w *streamWriter) *eventPuller {
	p := &eventPuller{src: src, w: w}
	if src.config.KeepaliveInterval <= 0 {
		return p
	}
	p.requests = make(chan struct{})
	p.results = make(chan eventResult)
	p.done = make(chan struct{})
	p.ticker = time.NewTicker(src.config.KeepaliveInterval)
	go func() {
		defer close(p.done)
		for {
			select {
			case <-src.ctx.Done():
				return
			case <-p.requests:
			}
			event, err := src.next()
			if pauseErr := src.pauseIdle(); err == nil {
				err = pauseErr
			}
			select {
			case <-src.ctx.Done():
				return
			case p.results <- eventResult{event, err}:
			}
		}
	}()
	return p
}

func (p *eventPuller) next() (*sseEvent, error) {
	if p.requests == nil {
		return p.src.next()
	}
	select {
	case <-p.src.ctx.Done():
		return nil, context.Cause(p.src.ctx)
	case p.requests <- struct{}{}:
	}
	for {
		select {
		case <-p.src.ctx.Done():
			return nil, context.Cause(p.src.ctx)
		case result := <-p.results:
			return result.event, result.err
		case <-p.ticker.C:
			if err := p.w.write(": gatemux keepalive\n\n"); err != nil {
				return nil, err
			}
		}
	}
}

func (p *eventPuller) close() {
	if p.ticker == nil {
		return
	}
	p.ticker.Stop()
	_ = p.src.Close()
	<-p.done
}
