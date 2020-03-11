package complcache

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync"
	"testing"
	"time"
)

func returnAfter(t *testing.T, value interface{}, d time.Duration) interface{} {
	time.Sleep(d)
	return value
}

func TestTimeout(t *testing.T) {
	// this test is racy; but what are you going to do when it comes to
	// testing timeouts?

	c, err := New(100*time.Millisecond, 100*time.Millisecond, 1*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// try to add item, but take too long

	_, err = c.GetOrCreate("key", func() (interface{}, error) {
		return returnAfter(t, "badval", 500*time.Millisecond), nil
	})

	if err != Timeout {
		t.Errorf("got bad err=%v", err)
	}
}

func TestHit(t *testing.T) {
	// create the cache

	c, err := New(1*time.Minute, 1*time.Minute, 1*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// add a bunch of values to cache

	wg := sync.WaitGroup{}
	wg.Add(1000)

	for i := 0; i < 1000; i += 1 {
		key := fmt.Sprintf("key%v", i)
		value := fmt.Sprintf("value%v", i)

		go c.GetOrCreate(key, func() (interface{}, error) {
			time.Sleep(100 * time.Millisecond)
			wg.Done()
			return value, nil
		})
	}

	wg.Wait()

	// get values from cache

	wg.Add(1000 * 2)

	for i := 0; i < 1000; i += 1 {
		key := fmt.Sprintf("key%v", i)
		valueExpected := fmt.Sprintf("value%v", i)

		go func() {
			valueActual, err := c.Get(key)

			if err != nil {
				t.Errorf("got err=%v for key=%v", err, key)
			}

			if valueActual != valueExpected {
				t.Errorf("got valueActual=%v valueExpected=%v", valueActual, valueExpected)
			}

			wg.Done()
		}()

		go func() {
			valueActual, err := c.GetOrCreate(key, func() (interface{}, error) {
				return nil, errors.New("should not have been called")
			})

			if err != nil {
				t.Errorf("got err=%v for key=%v", err, key)
			}

			if valueActual != valueExpected {
				t.Errorf("got valueActual=%v valueExpected=%v", valueActual, valueExpected)
			}

			wg.Done()
		}()
	}

	wg.Wait()
}

func TestExpire(t *testing.T) {
	// this test is racy; but what are you going to do when it comes to
	// testing timeouts?

	c, err := New(2*time.Second, 8*time.Second, 1*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	k := "key"
	v := "value"

	// add value to cache

	cv, err := c.GetOrCreate(k, func() (interface{}, error) {
		return returnAfter(t, v, 1*time.Second), nil
	})

	if err != nil {
		t.Error("should have added but failed with:", err)
	}

	if cv != v {
		t.Errorf("expected v=%v got cv=%v", v, cv)
	}

	// wait for it to expire

	time.Sleep(5 * time.Second)

	// check if it got deleted

	if cv, err := c.Get(k); err != NoSuchKey {
		t.Errorf("expected err=NoSuchKey but got cv=%v err=%v", cv, err)
	}
}

func TestClose(t *testing.T) {
	epsilon := 1 * time.Millisecond
	c, err := New(epsilon, epsilon, epsilon)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Close(); err != nil {
		t.Error("refusing to close:", err)
	}

	if _, err := c.Get("asdf"); err != Closed {
		t.Error("bad error:", err)
	}

	if _, err := c.GetOrCreate("asdf", nil); err != Closed {
		t.Error("bad error:", err)
	}

	if err := c.Close(); err == nil {
		t.Error("not reporting error on multiple Close")
	}
}

func TestBadArgs(t *testing.T) {
	if _, err := New(-1, 1, 2); err == nil {
		t.Error("accepted negative expiration argument")
	}

	if _, err := New(1, -1, 2); err == nil {
		t.Error("accepted negative fill argument")
	}

	if _, err := New(1, 1, -2); err == nil {
		t.Error("accepted negative gc argument")
	}
}

func TestReadme(t *testing.T) {
	// Create a new cache. (1) expiration is how long complcache
	// keeps a value until it is evicted. (2) fill controls
	// how long GetOrCreate waits for a new value to be available
	// before failing with a time out. (3) gc is the time between
	// garbage collection which cleans up expired values from
	// the cache

	expiration := 1 * time.Minute
	fill := 1 * time.Minute
	gc := 1 * time.Minute

	cache, err := New(expiration, fill, gc)
	if err != nil {
		panic(err) // New returns errors on bad arguments
	}

	// While you can just Get a value for a given key, it's best
	// to use GetOrCreate. If a cached value is available, that
	// value is returned. If no valid value is in the cache, creator
	// gets called to first create a value for the cache.

	creator := func() (interface{}, error) {
		// HTTP may be slow, so we cache it with complcache!
		resp, err := http.Get("http://example.com/")

		// Creator returns either some value (interface{}) or
		// an error. So we can just forward the error.
		if err != nil {
			return nil, err
		}

		// We got a value, return that instead.
		defer resp.Body.Close()
		return ioutil.ReadAll(resp.Body)
	}

	entry, err := cache.GetOrCreate("example.com", creator)
	doSomethingWith(entry, err)
}

func doSomethingWith(x ...interface{}) {
	// do nothing actually :)
}
