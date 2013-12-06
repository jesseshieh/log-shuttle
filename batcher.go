package main

import (
	"sync"
	"time"
)

func StartBatchers(config ShuttleConfig, drops *Counter, stats chan<- NamedValue, inLogs <-chan LogLine, inBatches <-chan *Batch, outBatches chan<- *Batch) *sync.WaitGroup {
	batchWaiter := new(sync.WaitGroup)
	for i := 0; i < config.NumBatchers; i++ {
		batchWaiter.Add(1)
		go func() {
			defer batchWaiter.Done()
			batcher := NewBatcher(config, drops, stats, inLogs, inBatches, outBatches)
			batcher.Batch()
		}()
	}

	return batchWaiter
}

type Batcher struct {
	inLogs     <-chan LogLine    // Where I get the log lines to batch from
	inBatches  <-chan *Batch     // Where I get empty batches from
	outBatches chan<- *Batch     // Where I send completed batches to
	stats      chan<- NamedValue // Where to send measurements
	drops      *Counter          // The drops counter
	config     ShuttleConfig
}

func NewBatcher(config ShuttleConfig, drops *Counter, stats chan<- NamedValue, inLogs <-chan LogLine, inBatches <-chan *Batch, outBatches chan<- *Batch) *Batcher {
	return &Batcher{
		inLogs:     inLogs,
		inBatches:  inBatches,
		drops:      drops,
		stats:      stats,
		outBatches: outBatches,
		config:     config,
	}
}

// Loops getting an empty batch and filling it.
func (batcher *Batcher) Batch() {

	for batch := range batcher.inBatches {
		closeDown := batcher.fillBatch(batch)
		if batch.MsgCount > 0 {
			batcher.stats <- NamedValue{value: float64(batch.MsgCount), name: "batch.msg.count"}
			select {
			case batcher.outBatches <- batch:
			// submitted into the delivery channel,
			// nothing to do here.
			default:
				//Unable to deliver into the delivery channel,
				//increment drops
				batcher.stats <- NamedValue{value: float64(batch.MsgCount), name: "batch.msg.dropped"}
				batcher.drops.Add(uint64(batch.MsgCount))
			}
		}
		if closeDown {
			break
		}
	}
}

// fillBatch coalesces individual log lines into batches. Delivery of the
// batch happens on timeout after at least one message is received
// or when the batch is full.
func (batcher *Batcher) fillBatch(batch *Batch) bool {
	// Fill the batch with log lines
	var line LogLine
	open := true

	timeout := time.NewTimer(batcher.config.WaitDuration)
	timeout.Stop()       // don't timeout until we actually have a log line
	defer timeout.Stop() // ensure timer is stopped when done
	defer func(t time.Time) { batcher.stats <- NamedValue{value: time.Since(t).Seconds(), name: "batch.fill"} }(time.Now())

	for {
		select {
		case <-timeout.C:
			return !open

		case line, open = <-batcher.inLogs:
			if !open {
				return !open
			}
			batch.Write(line)
			if batch.MsgCount == batcher.config.BatchSize {
				return !open
			}
			if batch.MsgCount == 1 {
				timeout.Reset(batcher.config.WaitDuration)
			}
		}
	}
}
