package store

const (
	defaultPageSize = 50
	maxPageSize     = 500
)

// NormalizePage clamps incoming limit/offset to safe defaults so handlers
// don't have to repeat the same boilerplate. limit=0 (or negative) means
// "default page size"; limit greater than maxPageSize is capped.
func NormalizePage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
