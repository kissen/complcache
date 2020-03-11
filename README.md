complcache
==========

Go package that provides a *completion cache*, a key-value cache
which generates values on demand.

Usage
-----

	// Create a new cache. (1) expiration is how long complcache
	// keeps a value until it is evicted. (2) fill controls
	// how long GetOrCreate waits for a new value to be available
	// before failing with a time out. (3) gc is the time between
	// garbage collection which cleans up expired values from
	// the cache

	expiration := 1 * time.Minute
	fill := 1 * time.Minute
	gc := 1 * time.Minute

	cache, err := complcache.New(expiration, fill, gc)
	if err != nil {
		panic(err)  // New returns errors on bad arguments
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
