package cuckoo

import (
	"encoding/binary"
	"hash"
	"math/rand"
	"sync"

	"github.com/spaolacci/murmur3"
)

const (
	defaultBucketSize   = 4
	defaultTotalBuckets = 4 << 20
	defaultMaxKicks     = 500
	seed                = 59053
)

// fingerprint of the item
type fingerprint uint16

// emptyFingerprint represents an empty fingerprint
var emptyFingerprint fingerprint

// bucket with b fingerprints per bucket
type bucket []fingerprint

// tempBytes used to temporarily store the fingerprint
var tempBytes = make([]byte, 2, 2)

// Filter is the cuckoo-filter
type Filter struct {
	count        uint32
	buckets      []bucket
	bucketSize   uint8
	totalBuckets uint32
	hash         hash.Hash32
	maxKicks     uint16

	// protects above fields
	mu *sync.RWMutex
}

// initBuckets initialises the buckets
func initBuckets(totalBuckets uint32, bucketSize uint8) []bucket {
	buckets := make([]bucket, totalBuckets, totalBuckets)
	for i := range buckets {
		buckets[i] = make([]fingerprint, bucketSize, bucketSize)
	}

	return buckets
}

// StdFilter returns Standard Cuckoo-Filter
func StdFilter() *Filter {
	return newFilter(defaultTotalBuckets, defaultBucketSize, murmur3.New32WithSeed(seed))
}

func newFilter(tb uint32, bs uint8, hash hash.Hash32) *Filter {
	return &Filter{
		buckets:      initBuckets(tb, bs),
		bucketSize:   bs,
		totalBuckets: tb,
		hash:         hash,
		maxKicks:     defaultMaxKicks,
		mu:           &sync.RWMutex{},
	}
}

func NewFilter(count uint32) *Filter {
	b := nextPowerOf2(count) / defaultBucketSize
	return newFilter(b, defaultBucketSize, murmur3.New32WithSeed(seed))
}

// nextPowerOf2 returns the next power 2 >= v
func nextPowerOf2(v uint32) (n uint32) {
	var i uint32
	for i = 2; i < 32; i++ {
		n = 1 << i
		if n >= v {
			break
		}
	}

	return n
}

// deleteFrom deletes fingerprint from bucket if exists
func deleteFrom(b bucket, fp fingerprint) bool {
	for i := range b {
		if b[i] != fp {
			continue
		}

		b[i] = emptyFingerprint
		return true
	}

	return false
}

// containsIn returns if the given fingerprint exists in bucket
func containsIn(b bucket, fp fingerprint) bool {
	for i := range b {
		if b[i] == fp {
			return true
		}
	}

	return false
}

// addToBucket will add fp to the bucket i in filter
func addToBucket(b bucket, fp fingerprint) bool {
	for j := range b {
		if b[j] != emptyFingerprint {
			continue
		}

		b[j] = fp
		return true
	}

	return false
}

// hashOf returns the 32-bit hash
func hashOf(x []byte, hash hash.Hash32) (uint32, []byte) {
	hash.Reset()
	hash.Write(x)
	h := hash.Sum32()
	return h, []byte{byte(h >> 24), byte(h >> 16), byte(h >> 8), byte(h)}
}

// fingerprintOf returns the fingerprint of x with size using hash
func fingerprintOf(xb []byte, hash hash.Hash32) (fp fingerprint, fph uint32) {
	fp = fingerprint(binary.BigEndian.Uint16(xb))
	fph, _ = hashOf(xb[:2], hash)
	return fp, fph
}

// indicesOf returns the indices of item x using given hash
func indicesOf(xh, fph, totalBuckets uint32) (i1, i2 uint32) {
	i1 = xh % totalBuckets
	i2 = (i1 ^ fph) % totalBuckets
	return i1, i2
}

// insert inserts the item into filter
func insert(f *Filter, x []byte) (ok bool) {
	xh, xb := hashOf(x, f.hash)
	fp, fph := fingerprintOf(xb, f.hash)
	i1, i2 := indicesOf(xh, fph, f.totalBuckets)

	defer func() {
		if ok {
			f.count++
		}
	}()

	if addToBucket(f.buckets[i1], fp) || addToBucket(f.buckets[i2], fp) {
		return true
	}

	ri := []uint32{i1, i2}[rand.Intn(2)]
	var k uint16
	for k = 0; k < f.maxKicks; k++ {
		ri, fp = replaceItem(f, ri, fp)
		if addToBucket(f.buckets[ri], fp) {
			return true
		}
	}

	return false
}

// replaceItem replaces fingerprint from i and returns the alternate index for kicked fingerprint
func replaceItem(f *Filter, i uint32, fp fingerprint) (j uint32, rfp fingerprint) {
	b := f.buckets[i]
	k := rand.Intn(len(b))
	rfp, b[k] = b[k], fp
	binary.BigEndian.PutUint16(tempBytes, uint16(fp))
	rfph, _ := hashOf(tempBytes, f.hash)
	j = (i ^ rfph) % f.totalBuckets
	return j, rfp
}

// lookup checks if the item x existence in filter
func lookup(f *Filter, x []byte) bool {
	xh, xb := hashOf(x, f.hash)
	fp, fph := fingerprintOf(xb, f.hash)
	i1, i2 := indicesOf(xh, fph, f.totalBuckets)

	if containsIn(f.buckets[i1], fp) || containsIn(f.buckets[i2], fp) {
		return true
	}

	return false
}

// deleteItem deletes item if present from the filter
func deleteItem(f *Filter, x []byte) (ok bool) {
	xh, xb := hashOf(x, f.hash)
	fp, fph := fingerprintOf(xb, f.hash)
	i1, i2 := indicesOf(xh, fph, f.totalBuckets)

	defer func() {
		if ok {
			f.count--
		}
	}()

	if deleteFrom(f.buckets[i1], fp) || deleteFrom(f.buckets[i2], fp) {
		return true
	}

	return false
}

// check the bytes
func check(x []byte) ([]byte, bool) {
	if len(x) == 0 {
		return nil, false
	}

	if len(x) == 1 {
		x = []byte{0, x[0]}
	}

	return x, true
}

// Insert inserts the item to the filter
// returns error of filter is full
func (f *Filter) Insert(x []byte) bool {
	x, ok := check(x)
	if !ok {
		return false
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	return insert(f, x)
}

// InsertUnique inserts only unique items
func (f *Filter) InsertUnique(x []byte) bool {
	x, ok := check(x)
	if !ok {
		return false
	}

	if f.Lookup(x) {
		return true
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	return insert(f, x)
}

// Lookup says if the given item exists in filter
func (f *Filter) Lookup(x []byte) bool {
	x, ok := check(x)
	if !ok {
		return false
	}

	f.mu.RLock()
	defer f.mu.RUnlock()

	return lookup(f, x)
}

// Delete deletes the item from the filter
func (f *Filter) Delete(x []byte) bool {
	x, ok := check(x)
	if !ok {
		return false
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	return deleteItem(f, x)
}

// Count returns total inserted items into filter
func (f *Filter) Count() uint32 {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return f.count
}

// LoadFactor returns the load factor of the filter
func (f *Filter) LoadFactor() float64 {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return float64(f.count) / (float64(uint32(f.bucketSize) * f.totalBuckets))
}
