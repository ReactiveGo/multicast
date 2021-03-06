package multicast

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

//jig:template ChannelError

type ChannelError string

func (e ChannelError) Error() string { return string(e) }

//jig:template ErrOutOfEndpoints
//jig:needs ChannelError

// ErrOutOfEndpoints is returned by NewEndpoint when the maximum number of
// endpoints has already been created.
const ErrOutOfEndpoints = ChannelError("out of endpoints")

//jig:template ChanPadding

const _PADDING = 1            // 0 turns padding off, 1 turns it on.
const _EXTRA_PADDING = 0 * 64 // multiples of 64, benefits inconclusive.

type pad60 [_PADDING * (_EXTRA_PADDING + 60)]byte
type pad56 [_PADDING * (_EXTRA_PADDING + 56)]byte
type pad48 [_PADDING * (_EXTRA_PADDING + 48)]byte
type pad40 [_PADDING * (_EXTRA_PADDING + 40)]byte
type pad32 [_PADDING * (_EXTRA_PADDING + 32)]byte

//jig:template ChanState

// Activity of committer
const (
	resting uint32 = iota
	working
)

// Activity of endpoints
const (
	idling uint32 = iota
	enumerating
	creating
)

// State of endpoint and channel
const (
	active uint64 = iota
	canceled
	closed
)

// Cursor is parked so it does not influence advancing the commit index.
const (
	parked uint64 = math.MaxUint64
)

const (
	// ReplayAll can be passed to NewEndpoint to retain as many of the
	// previously sent messages as possible that are still in the buffer.
	ReplayAll uint64 = math.MaxUint64
)

//jig:template Chan<Foo>
//jig:needs ChanPadding, ChanState

// ChanFoo is a fast, concurrent multi-(casting,sending,receiving) buffered
// channel. It is implemented using only sync/atomic operations. Spinlocks using
// runtime.Gosched() are used in situations where goroutines are waiting or
// contending for resources.
type ChanFoo struct {
	buffer     []foo
	_________a pad40
	begin      uint64
	_________b pad56
	end        uint64
	_________c pad56
	commit     uint64
	_________d pad56
	mod        uint64
	_________e pad56
	endpoints  endpointsFoo

	// ChanFoo State

	err           error
	____________f pad48
	channelState  uint64 // active, closed
	____________g pad56

	write              uint64
	_________________h pad56
	start              time.Time
	_________________i pad40
	written            []int64 // nanoseconds since start
	_________________j pad40
	committerActivity  uint32 // resting, working
	_________________k pad60

	receivers          *sync.Cond
	_________________l pad56
}

type endpointsFoo struct {
	entry             []EndpointFoo
	len               uint32
	endpointsActivity uint32 // idling, enumerating, creating
	________          pad32
}

//jig:template Endpoint<Foo>
//jig:embeds Chan<Foo>

// EndpointFoo is returned by a call to NewEndpoint on the channel. Every
// endpoint should be used by only a single goroutine, so no sharing between
// goroutines.
type EndpointFoo struct {
	*ChanFoo
	_____________a pad56
	cursor         uint64
	_____________b pad56
	endpointState  uint64 // active, canceled, closed
	_____________c pad56
	lastActive     time.Time // track activity to deterime when to sleep
	_____________d pad40
	endpointClosed uint64 // active, closed
	_____________e pad56
}

//jig:template NewChan<Foo>
//jig:needs Chan<Foo>, endpoints<Foo>

// NewChanFoo creates a new channel. The parameters bufferCapacity and
// endpointCapacity determine the size of the message buffer and maximum
// number of concurrent receiving endpoints respectively.
//
// Note that bufferCapacity is always scaled up to a power of 2 so e.g.
// specifying 400 will create a buffer of 512 (2^9). Also because of this a
// bufferCapacity of 0 is scaled up to 1 (2^0).
func NewChanFoo(bufferCapacity int, endpointCapacity int) *ChanFoo {
	// Round capacity up to power of 2
	size := uint64(1) << uint(math.Ceil(math.Log2(float64(bufferCapacity))))
	c := &ChanFoo{
		end:     size,
		mod:     size - 1,
		buffer:  make([]foo, size),
		start:   time.Now(),
		written: make([]int64, size),
		endpoints: endpointsFoo{
			entry: make([]EndpointFoo, endpointCapacity),
		},
	}
	c.receivers = sync.NewCond(c)
	return c
}

// Lock, empty method so we can pass *ChanFoo to sync.NewCond as a Locker.
func (c *ChanFoo) Lock() {}

// Unlock, empty method so we can pass *ChanFoo to sync.NewCond as a Locker.
func (c *ChanFoo) Unlock() {}

//jig:template Chan<Foo> Close

// Close will close the channel. Pass in an error or nil. Endpoints  continue to
// receive data until the buffer is empty. Only then will the close notification
// be delivered to the Range function.
func (c *ChanFoo) Close(err error) {
	if atomic.CompareAndSwapUint64(&c.channelState, active, closed) {
		c.err = err
		c.endpoints.Access(func(endpoints *endpointsFoo) {
			for i := uint32(0); i < endpoints.len; i++ {
				atomic.CompareAndSwapUint64(&endpoints.entry[i].endpointState, active, closed)
			}
		})
	}
	c.receivers.Broadcast()
}

//jig:template Chan<Foo> Closed

// Closed returns true when the channel was closed using the Close method.
func (c *ChanFoo) Closed() bool {
	return atomic.LoadUint64(&c.channelState) >= closed
}

//jig:template Chan<Foo> FastSend
//jig:needs endpoints<Foo>, Chan<Foo> slideBuffer

// FastSend can be used to send values to the channel from a SINGLE goroutine.
// Also, this does not record the time a message was sent, so the maxAge value
// passed to Range will be ignored.
//
// Note, that when the number of unread messages has reached bufferCapacity, then
// the call to FastSend will block until the slowest Endpoint has read another
// message.
func (c *ChanFoo) FastSend(value foo) {
	for c.commit == c.end {
		if !c.slideBuffer() {
			return // channel was closed
		}
	}
	c.buffer[c.commit&c.mod] = value
	atomic.AddUint64(&c.commit, 1)
	c.receivers.Broadcast()
}

//jig:template Chan<Foo> Send
//jig:needs endpoints<Foo>, Chan<Foo> slideBuffer

// Send can be used by concurrent goroutines to send values to the channel.
//
// Note, that when the number of unread messages has reached bufferCapacity, then
// the call to Send will block until the slowest Endpoint has read another
// message.
func (c *ChanFoo) Send(value foo) {
	write := atomic.AddUint64(&c.write, 1) - 1
	for write >= atomic.LoadUint64(&c.end) {
		if !c.slideBuffer() {
			return // channel was closed
		}
	}
	c.buffer[write&c.mod] = value
	updated := time.Since(c.start).Nanoseconds()
	if updated == 0 {
		panic("clock failure; zero duration measured")
	}
	atomic.StoreInt64(&c.written[write&c.mod], updated<<1+1)
	c.receivers.Broadcast()
}

//jig:template Chan<Foo> slideBuffer
//jig:needs endpoints<Foo>

func (c *ChanFoo) slideBuffer() bool {
	slowestCursor := parked
	spinlock := c.endpoints.Access(func(endpoints *endpointsFoo) {
		for i := uint32(0); i < endpoints.len; i++ {
			cursor := atomic.LoadUint64(&endpoints.entry[i].cursor)
			if cursor < slowestCursor {
				slowestCursor = cursor
			}
		}
		if atomic.LoadUint64(&c.begin) < slowestCursor && slowestCursor <= atomic.LoadUint64(&c.end) {
			if c.mod < 16 {
				atomic.AddUint64(&c.begin, 1)
				atomic.AddUint64(&c.end, 1)
			} else {
				atomic.StoreUint64(&c.begin, slowestCursor)
				atomic.StoreUint64(&c.end, slowestCursor+c.mod+1)
			}
		} else {
			slowestCursor = parked
		}
	})
	if slowestCursor == parked {
		if spinlock {
			runtime.Gosched() // spinlock while full
		}
		if atomic.LoadUint64(&c.channelState) != active {
			return false // !more
		}
	}
	return true // more
}

//jig:template Chan<Foo> commitData

func (c *ChanFoo) commitData() uint64 {
	commit := atomic.LoadUint64(&c.commit)
	if commit >= atomic.LoadUint64(&c.write) {
		return commit
	}
	if !atomic.CompareAndSwapUint32(&c.committerActivity, resting, working) {
		return commit // allow only a single receiver goroutine at a time
	}
	commit = atomic.LoadUint64(&c.commit)
	newcommit := commit
	for ; atomic.LoadInt64(&c.written[newcommit&c.mod])&1 == 1; newcommit++ {
		atomic.AddInt64(&c.written[newcommit&c.mod], -1)
		if newcommit >= atomic.LoadUint64(&c.end) {
			break
		}
	}
	write := atomic.LoadUint64(&c.write)
	if newcommit > write {
		panic(fmt.Sprintf("commitData: range error (commit=%d,write=%d,newcommit=%d)", commit, write, newcommit))
	}
	if newcommit > commit {
		if !atomic.CompareAndSwapUint64(&c.commit, commit, newcommit) {
			panic(fmt.Sprintf("commitData; swap error (c.commit=%d,%d,%d)", c.commit, commit, newcommit))
		}
		c.receivers.Broadcast() // fresh data! wakeup blocked receiver goroutines
	}
	atomic.StoreUint32(&c.committerActivity, resting)
	return atomic.LoadUint64(&c.commit)
}

//jig:template Chan<Foo> NewEndpoint
//jig:needs endpoints<Foo>

// NewEndpoint will create a new channel endpoint that can be used to receive
// from the channel. The argument keep specifies how many entries of the
// existing channel buffer to keep.
//
// After Close is called on the channel, any endpoints created after that
// will still receive the number of messages as indicated in the keep parameter
// and then subsequently the close.
//
// An endpoint that is canceled or read until it is exhausted (after channel was
// closed) will be reused by NewEndpoint.
func (c *ChanFoo) NewEndpoint(keep uint64) (*EndpointFoo, error) {
	return c.endpoints.NewForChanFoo(c, keep)
}

//jig:template endpoints<Foo>
//jig:needs Chan<Foo>, ErrOutOfEndpoints

func (e *endpointsFoo) NewForChanFoo(c *ChanFoo, keep uint64) (*EndpointFoo, error) {
	for !atomic.CompareAndSwapUint32(&e.endpointsActivity, idling, creating) {
		runtime.Gosched()
	}
	defer atomic.StoreUint32(&e.endpointsActivity, idling)
	var start uint64
	commit := c.commitData()
	begin := atomic.LoadUint64(&c.begin)
	if commit-begin <= keep {
		start = begin
	} else {
		start = commit - keep
	}
	if int(e.len) == len(e.entry) {
		for index := uint32(0); index < e.len; index++ {
			ep := &e.entry[index]
			if atomic.CompareAndSwapUint64(&ep.cursor, parked, start) {
				ep.endpointState = atomic.LoadUint64(&c.channelState)
				ep.lastActive = time.Now()
				return ep, nil
			}
		}
		return nil, ErrOutOfEndpoints
	}
	ep := &e.entry[e.len]
	ep.ChanFoo = c
	ep.cursor = start
	ep.endpointState = atomic.LoadUint64(&c.channelState)
	ep.lastActive = time.Now()
	e.len++
	return ep, nil
}

func (e *endpointsFoo) Access(access func(*endpointsFoo)) bool {
	contention := false
	for !atomic.CompareAndSwapUint32(&e.endpointsActivity, idling, enumerating) {
		runtime.Gosched()
		contention = true
	}
	access(e)
	atomic.StoreUint32(&e.endpointsActivity, idling)
	return !contention
}

//jig:template Endpoint<Foo> Range
//jig:needs Endpoint<Foo>

// Range will call the passed in foreach function with all the messages in
// the buffer, followed by all the messages received. When the foreach function
// returns true Range will continue, when you return false this is the same as
// calling Cancel. When canceled the foreach will never be called again.
// Passing a maxAge duration other than 0 will skip messages that are older
// than maxAge.
//
// When the channel is closed, eventually when the buffer is exhausted the close
// with optional error will be notified by calling foreach one last time with
// the closed parameter set to true.
func (e *EndpointFoo) Range(foreach func(value foo, err error, closed bool) bool, maxAge time.Duration) {
	e.lastActive = time.Now()
	for {
		commit := e.commitData()
		for ; e.cursor == commit; commit = e.commitData() {
			if atomic.CompareAndSwapUint64(&e.endpointState, canceled, canceled) {
				atomic.StoreUint64(&e.cursor, parked)
				return
			}
			if atomic.LoadUint64(&e.commit) < atomic.LoadUint64(&e.write) {
				if e.endpointClosed == 1 {
					panic(fmt.Sprintf("data written after closing endpoint; commit(%d) write(%d)",
						atomic.LoadUint64(&e.commit), atomic.LoadUint64(&e.write)))
				}
				runtime.Gosched() // just backoff a little ~1us
				e.lastActive = time.Now()
			} else {
				now := time.Now()
				if now.Before(e.lastActive.Add(1 * time.Millisecond)) {
					if atomic.CompareAndSwapUint64(&e.endpointState, closed, closed) {
						e.endpointClosed = 1 // note close happened, but don't close yet.
					}
					runtime.Gosched() // 0<lastActive<1ms: just backoff a little ~1us
				} else if now.Before(e.lastActive.Add(250 * time.Millisecond)) {
					if atomic.CompareAndSwapUint64(&e.endpointState, closed, closed) {
						var zero foo
						foreach(zero, e.err, true)
						atomic.StoreUint64(&e.cursor, parked)
						return //we're done
					}
					runtime.Gosched() // 1ms<lastActive<250ms: just backoff a little ~1us
				} else {
					e.receivers.Wait() // 250ms<lastActive: block on condition
					e.lastActive = time.Now()
				}
			}
		}
		// process data we got
		for ; e.cursor != commit; atomic.AddUint64(&e.cursor, 1) {
			item := e.buffer[e.cursor&e.mod]
			emit := true
			if maxAge != 0 {
				stale := time.Since(e.start).Nanoseconds() - maxAge.Nanoseconds()
				updated := atomic.LoadInt64(&e.written[e.cursor&e.mod]) >> 1
				if updated != 0 && updated <= stale {
					emit = false
				}
			}
			if emit && !foreach(item, nil, false) {
				atomic.StoreUint64(&e.endpointState, canceled)
			}
			if atomic.LoadUint64(&e.endpointState) == canceled {
				atomic.StoreUint64(&e.cursor, parked)
				return
			}
		}
		e.lastActive = time.Now()
	}
}

//jig:template Endpoint<Foo> Cancel
//jig:needs Endpoint<Foo>

// Cancel cancels the endpoint, making it available to be reused when
// NewEndpoint is called on the channel. When canceled the foreach function
// passed to Range is not notified, instead just never called again.
func (e *EndpointFoo) Cancel() {
	atomic.CompareAndSwapUint64(&e.endpointState, active, canceled)
	e.receivers.Broadcast()
}
