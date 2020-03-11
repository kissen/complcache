// Implement a cache for long running or otherwise expensive operations. This "completion
// cache" will return either the most recent cached value or start the long operation
// from scratch if the old value has expired.
//
// Using the three constructor parameters (expiration, fill and gc) you can fine-tune the behavior of this
// cache.
package complcache

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	Timeout   = errors.New("creator timed out")
	Closed    = errors.New("cache closed")
	NoSuchKey = errors.New("no such key")
)

// A completion cache for arbitrary values sorted by some key.
type Cache interface {
	// Return the value with matching key. Returns complcache.NoSuchKey if no such element
	// currently exists in the cache. Returns complcache.Timeout if there was a call to
	// GetOrCreate trying to retrieve a value for key, but that call timed out.
	//
	// If the cache is closed (after a call to Close), this method will return the
	// complcache.Closed error.
	Get(key interface{}) (obj interface{}, err error)

	// Return the value with matching key. If no element with matching key exists,
	// run creator in a separate goroutine to acquire that value.
	//
	// Note that after the "fill" timeout has passed (defined when constructing this cache
	// with New), this method will return complcache.Timeout. Despite this, the goroutine
	// running the creator function will not be interrupted. Make sure your creator functions
	// eventually exits, otherwise you are stuck with dead goroutines.
	//
	// If the cache is closed (after a call to Close), this method will return
	// the complcache.Closed error.
	GetOrCreate(key interface{}, creator func() (interface{}, error)) (obj interface{}, err error)

	// Close the cache, that is stop the goroutine that regularly evicts expired values
	// from the cache.
	//
	// It is an error to call Get or GetOrCreate after a call to Close. That said, already
	// running versions of Get Or GetOrCreate will complete.
	Close() error
}

type cache struct {
	// global lock on cache
	sync.Mutex

	// how long to keep fresh values
	expiration time.Duration

	// how long to wait until GetOrCreate returns Timeout
	fill time.Duration

	// time to wait between garbage collection steps
	gc time.Duration

	// the actual cached values
	contents map[interface{}]*entry

	// whether the cache is closed; it's an error to keep using
	// the cache after the cache has been closed
	closed bool
}

type entry struct {
	// (object, err) are the actually cached payloads
	object interface{}
	err    error

	// the cache that owns this entry; we need this to get
	// the timeouts from Cache
	parent *cache

	// true once fetching was completed
	present bool

	// time on which fetching this resource started;
	// used to determine whether an entry is expired
	requestedOn time.Time

	// synchronization aid that allow goroutines to wait
	// on the result
	cond *sync.Cond
}

// Create a new completion cache.
//
// This constructor takes three timeout value that control the behavior of this
// cache. expiration is the duration until a fresh value is evicted from the cache.
// fill controls how long GetOrCreate waits for its argument creator to yield a new
// value. gc is the time to wait between garbage collection steps.
//
// expiration, fill and gc may not be negative or zero.
func New(expiration, fill, gc time.Duration) (Cache, error) {
	// check args

	if expiration <= 0 {
		return nil, fmt.Errorf("invalid argument expiration=%v", expiration)
	}

	if fill <= 0 {
		return nil, fmt.Errorf("invalid argument fill=%v", fill)
	}

	if gc <= 0 {
		return nil, fmt.Errorf("invalid argument gc=%v", gc)
	}

	// create cache

	ch := &cache{
		expiration: expiration,
		fill:       fill,
		gc:         gc,
		contents:   make(map[interface{}]*entry),
	}

	go ch.clean()

	return ch, nil
}

func (vc *cache) Get(key interface{}) (obj interface{}, err error) {
	// only one concurrent access of the map at a given time

	vc.Lock()

	// cannot get from closed map

	if vc.closed {
		vc.Unlock()
		return nil, Closed
	}

	// try to acquire an entry with matching key

	ent, ok := vc.contents[key]

	if !ok || ent.Expired() {
		vc.Unlock()
		return nil, NoSuchKey
	}

	// got an entry; wait on the result from that entry without
	// keeping the whole map locked

	vc.Unlock()
	return ent.Get()
}

func (vc *cache) GetOrCreate(key interface{}, creator func() (interface{}, error)) (obj interface{}, err error) {
	// only one concurrent access of the map at a given time

	vc.Lock()

	// cannot get from closed map

	if vc.closed {
		vc.Unlock()
		return nil, Closed
	}

	// get entry from map; maybe we can just return the value right away

	ent, ok := vc.contents[key]

	if ok && !ent.Expired() {
		vc.Unlock()
		return ent.Get()
	}

	// no entry or only an expired one; create a new one

	ent = &entry{
		object:      nil,
		err:         nil,
		parent:      vc,
		present:     false,
		requestedOn: time.Now(),
		cond:        sync.NewCond(&sync.Mutex{}),
	}

	// add entry to map for retrieval by other goroutines

	vc.contents[key] = ent

	// start the function that's supposed to yield us a value

	go ent.Fill(creator)

	// wait on the result without keeping the whole map locked

	vc.Unlock()
	return ent.Get()
}

func (vc *cache) Close() error {
	vc.Lock()
	defer vc.Unlock()

	if vc.closed {
		return errors.New("already closed")
	} else {
		vc.closed = true
		return nil
	}
}

// Remove objects from the cache we consider expired. Keeps
// running until the cache is Close()d.
func (vc *cache) clean() {
	for !vc.closed {
		vc.Lock()

		for key, value := range vc.contents {
			if value.Expired() {
				delete(vc.contents, key)
			}
		}

		vc.Unlock()

		time.Sleep(vc.gc)
	}
}

// Go and fill the underlying entry with iri.
func (e *entry) Fill(creator func() (interface{}, error)) {
	// wrap results from creator into a struct called result

	type result struct {
		obj interface{}
		err error
	}

	// start the creator in a separate routine; its result is written
	// to channel c

	c := make(chan result)

	go func() {
		var r result
		r.obj, r.err = creator()
		c <- r
		close(c)
	}()

	// either wait for the result to be returned from the creator or
	// until the timeout hits

	var r result

	select {
	case r = <-c:
		break

	case <-time.After(e.parent.fill):
		r.obj = nil
		r.err = Timeout
	}

	// now that we have some result; publish it

	e.cond.L.Lock()
	defer e.cond.L.Unlock()

	e.object = r.obj
	e.err = r.err
	e.present = true

	e.cond.Broadcast()
}

// Return the underlying object/error or wait for it first if it
// isn't available yet.
func (e *entry) Get() (interface{}, error) {
	e.cond.L.Lock()

	// for each entry, we only expect exactly one Broadcast;
	// if after a wakeup from Wait() the value is not present,
	// something is seriously wrong

	for !e.present {
		e.cond.Wait()
	}

	e.cond.L.Unlock()

	return e.object, e.err
}

// Returns whether this entry is expired.
func (e *entry) Expired() bool {
	expiredOn := e.requestedOn.Add(e.parent.expiration)
	return time.Now().After(expiredOn)
}
