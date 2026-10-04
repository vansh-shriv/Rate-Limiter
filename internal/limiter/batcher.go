package limiter

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Batcher is a redis.Scripter that coalesces concurrent EVALSHA calls into pipelines.
//
// Why: each decision is one Redis round trip. Under load, hundreds of requests are in flight at once;
// sending them one at a time spends most of the time on per-command network and syscall overhead.
// A small number of flusher goroutines each take whatever is already queued (up to maxBatch) and send it as
// ONE pipeline, so throughput scales with batch size while Redis still executes every script atomically
// and in order. Each script remains independently atomic; only the network trips are shared.
//
// Adaptive by design: a flusher never waits for a batch to fill. At low load a batch is a single command
// (no added latency); batches grow only when requests are already queuing up.
type Batcher struct {
	redis.Scripter // Eval, ScriptLoad, ... pass straight through

	rdb      *redis.Client
	reqs     chan *batchReq
	maxBatch int
	timeout  time.Duration
	wg       sync.WaitGroup
	stop     chan struct{}
}

type batchReq struct {
	sha  string
	keys []string
	args []any
	res  chan *redis.Cmd
}

// NewBatcher starts `flushers` goroutines. timeout bounds one pipeline round trip.
func NewBatcher(rdb *redis.Client, flushers, maxBatch int, timeout time.Duration) *Batcher {
	b := &Batcher{
		Scripter: rdb, rdb: rdb, maxBatch: maxBatch, timeout: timeout,
		reqs: make(chan *batchReq, flushers*maxBatch), stop: make(chan struct{}),
	}
	for i := 0; i < flushers; i++ {
		b.wg.Add(1)
		go b.flushLoop()
	}
	return b
}

func (b *Batcher) Close() {
	close(b.stop)
	b.wg.Wait()
}

// EvalSha queues the call and waits for its own result from the shared pipeline.
// It never blocks forever: a closed Batcher fails fast, and a waiter is released if Close races its request
// (the queue is buffered, so a send can succeed after the flushers have exited).
func (b *Batcher) EvalSha(ctx context.Context, sha string, keys []string, args ...any) *redis.Cmd {
	select {
	case <-b.stop:
		return failed(ctx, redis.ErrClosed)
	default:
	}
	req := &batchReq{sha: sha, keys: keys, args: args, res: make(chan *redis.Cmd, 1)}
	select {
	case b.reqs <- req:
	case <-ctx.Done():
		return failed(ctx, ctx.Err())
	case <-b.stop:
		return failed(ctx, redis.ErrClosed)
	}
	select {
	case cmd := <-req.res:
		return cmd
	case <-ctx.Done():
		return failed(ctx, ctx.Err())
	case <-b.stop:
		select { // a flusher may have answered just before shutdown
		case cmd := <-req.res:
			return cmd
		default:
			return failed(ctx, redis.ErrClosed)
		}
	}
}

func failed(ctx context.Context, err error) *redis.Cmd {
	cmd := redis.NewCmd(ctx)
	cmd.SetErr(err)
	return cmd
}

func (b *Batcher) flushLoop() {
	defer b.wg.Done()
	batch := make([]*batchReq, 0, b.maxBatch)
	for {
		batch = batch[:0]
		select {
		case r := <-b.reqs:
			batch = append(batch, r)
		case <-b.stop:
			return
		}
	drain: // take only what is already waiting; never block to "fill" a batch
		for len(batch) < b.maxBatch {
			select {
			case r := <-b.reqs:
				batch = append(batch, r)
			default:
				break drain
			}
		}
		b.send(batch)
	}
}

func (b *Batcher) send(batch []*batchReq) {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	pipe := b.rdb.Pipeline()
	cmds := make([]*redis.Cmd, len(batch))
	for i, r := range batch {
		cmds[i] = pipe.EvalSha(ctx, r.sha, r.keys, r.args...)
	}
	_, _ = pipe.Exec(ctx) // per-command errors live on each cmd (incl. NOSCRIPT, which Script.Run handles)
	for i, r := range batch {
		r.res <- cmds[i]
	}
}
