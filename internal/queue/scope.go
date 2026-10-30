package queue

// SkipLabel is owned exclusively by the repository owner.
const SkipLabel = "clawflow-skip"

// Remove returns order without k.
func Remove(order []Key, k Key) []Key {
	out := make([]Key, 0, len(order))
	for _, o := range order {
		if o != k {
			out = append(out, o)
		}
	}
	return out
}
